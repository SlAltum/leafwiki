//go:build vectors

package embed

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

// EncoderOptions configures the inference session. The zero value works once
// SharedLibraryPath is set.
type EncoderOptions struct {
	// SharedLibraryPath is the libonnxruntime shared library to load. The
	// binding and the library must agree on the ONNX Runtime API version, so
	// this is configuration rather than a guess: a mismatch fails at startup
	// with a version message.
	SharedLibraryPath string
	// InputNames defaults to the three BERT inputs.
	InputNames []string
	// OutputName defaults to the token-level output mean pooling needs.
	OutputName string
	// MaxTokens caps the sequence length; 0 takes the manifest's value.
	MaxTokens int
	// IntraOpThreads bounds the runtime's own worker pool. The default spins up
	// one worker per core and keeps them busy-waiting between calls, which
	// starves everything else in the process; a single thread is plenty for a
	// query and still fast for a build. 0 leaves the runtime's default.
	IntraOpThreads int
}

var defaultInputNames = []string{"input_ids", "attention_mask", "token_type_ids"}

// defaultOutputName is the token-level output of a sentence-transformer
// export; pooling runs here, so the graph stays as small as possible.
const defaultOutputName = "last_hidden_state"

// ONNX Runtime is a process-wide singleton, so it is started once.
var (
	environmentOnce sync.Once
	environmentErr  error
)

// ErrEncoderNotBuilt is returned by a build compiled without the vectors tag.
var ErrEncoderNotBuilt = errors.New("embed: this build was compiled without the vectors tag")

// Encoder runs the packaged model in this process through ONNX Runtime.
type Encoder struct {
	session   *ort.DynamicAdvancedSession
	tokenizer *WordPieceTokenizer
	options   EncoderOptions
	dim       int
	maxTokens int
	// pooling is how the model was trained to turn tokens into one vector.
	pooling string
	// queryPrefix is prepended to queries only (retrieval models often ask
	// for an instruction on the query side and nothing on the document side).
	queryPrefix string

	// mu serializes Run: one session is not safe for concurrent calls, and a
	// second session would load the weights twice (NFR-5).
	mu sync.Mutex
}

// EncoderAvailable reports whether this build can run the model at all.
func EncoderAvailable() bool { return true }

// NewEncoder verifies the model directory, starts the runtime and returns an
// encoder for it.
func NewEncoder(model *Model, options EncoderOptions) (*Encoder, error) {
	if model == nil {
		return nil, fmt.Errorf("%w: no model", ErrModelUnavailable)
	}
	if err := model.Verify(); err != nil {
		return nil, err
	}
	if options.SharedLibraryPath == "" {
		return nil, fmt.Errorf("embed: no ONNX Runtime shared library configured")
	}

	if len(options.InputNames) == 0 {
		options.InputNames = defaultInputNames
	}
	if options.OutputName == "" {
		options.OutputName = defaultOutputName
	}
	if options.MaxTokens <= 0 {
		options.MaxTokens = model.Manifest.MaxTokens
	}
	if options.MaxTokens <= 0 {
		return nil, fmt.Errorf("%w: max_tokens is not set", ErrInvalidManifest)
	}

	tokenizer, err := LoadWordPieceTokenizer(filepath.Join(model.Dir, "tokenizer.json"))
	if err != nil {
		return nil, err
	}

	environmentOnce.Do(func() {
		ort.SetSharedLibraryPath(options.SharedLibraryPath)
		environmentErr = ort.InitializeEnvironment()
	})
	if environmentErr != nil {
		return nil, fmt.Errorf("embed: starting ONNX Runtime: %w", environmentErr)
	}

	created, err := ort.NewSessionOptions()
	if err != nil {
		return nil, fmt.Errorf("embed: preparing session options: %w", err)
	}
	sessionOptions := created
	defer func() { _ = sessionOptions.Destroy() }()

	if options.IntraOpThreads > 0 {
		if err := sessionOptions.SetIntraOpNumThreads(options.IntraOpThreads); err != nil {
			return nil, fmt.Errorf("embed: setting the intra-op thread count: %w", err)
		}
		if err := sessionOptions.SetInterOpNumThreads(options.IntraOpThreads); err != nil {
			return nil, fmt.Errorf("embed: setting the inter-op thread count: %w", err)
		}
	}

	// ponytail: no CPU arena and no memory pattern. They make repeated inference
	// faster, but the arena keeps its peak after a full rebuild, and a wiki
	// indexes a few pages at a time once the initial build is done. Re-enable
	// them if bulk indexing ever becomes the common path and the footprint is
	// the smaller cost.
	if err := sessionOptions.SetCpuMemArena(false); err != nil {
		return nil, fmt.Errorf("embed: disabling the CPU arena: %w", err)
	}
	if err := sessionOptions.SetMemPattern(false); err != nil {
		return nil, fmt.Errorf("embed: disabling the memory pattern: %w", err)
	}

	session, err := ort.NewDynamicAdvancedSession(
		filepath.Join(model.Dir, onnxFileName(model.Manifest)),
		options.InputNames,
		[]string{options.OutputName},
		sessionOptions,
	)
	if err != nil {
		return nil, fmt.Errorf("embed: opening the model: %w", err)
	}

	return &Encoder{
		session:     session,
		tokenizer:   tokenizer,
		options:     options,
		dim:         model.Manifest.Dim,
		maxTokens:   options.MaxTokens,
		pooling:     model.Manifest.Pooling,
		queryPrefix: model.Manifest.QueryPrefix,
	}, nil
}

// Close releases the session.
func (e *Encoder) Close() error {
	if e == nil || e.session == nil {
		return nil
	}
	return e.session.Destroy()
}

// EncodeQuery returns the vector of a query, with the model's query prefix
// applied. Documents must go through Encode: the two sides are not symmetric.
func (e *Encoder) EncodeQuery(query string) ([]float32, error) {
	vectors, err := e.Encode([]string{e.queryPrefix + query})
	if err != nil || len(vectors) == 0 {
		return nil, err
	}
	return vectors[0], nil
}

// Encode returns one L2-normalized vector per text.
// maxBatchSequences bounds how many passages one model call sees. A page with
// hundreds of passages would otherwise allocate an output tensor of
// batch x tokens x dimensions in one go - hundreds of megabytes the model then
// works through slower than the same work split up.
const maxBatchSequences = 32

// Encode returns one vector per text, in input order.
func (e *Encoder) Encode(texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	vectors := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += maxBatchSequences {
		end := start + maxBatchSequences
		if end > len(texts) {
			end = len(texts)
		}
		batchVectors, err := e.encodeBatch(texts[start:end])
		if err != nil {
			return nil, err
		}
		vectors = append(vectors, batchVectors...)
	}
	return vectors, nil
}

// encodeBatch runs one model call over up to maxBatchSequences texts.
func (e *Encoder) encodeBatch(texts []string) ([][]float32, error) {

	sequences := make([][]int32, len(texts))
	seqLen := 0
	for i, text := range texts {
		encoding := e.tokenizer.Encode(text, e.maxTokens)
		sequences[i] = encoding.IDs
		if len(encoding.IDs) > seqLen {
			seqLen = len(encoding.IDs)
		}
	}
	if seqLen == 0 {
		return nil, fmt.Errorf("embed: empty input")
	}

	batch := int64(len(texts))
	shape := ort.NewShape(batch, int64(seqLen))
	ids := make([]int64, batch*int64(seqLen))
	mask := make([]int64, len(ids))
	types := make([]int64, len(ids))
	for i, sequence := range sequences {
		for j := 0; j < seqLen; j++ {
			position := i*seqLen + j
			if j < len(sequence) {
				ids[position] = int64(sequence[j])
				mask[position] = 1
			}
		}
	}

	inputs, err := e.newInputs(shape, ids, mask, types)
	if err != nil {
		return nil, err
	}
	defer releaseValues(inputs)

	output, err := ort.NewEmptyTensor[float32](ort.NewShape(batch, int64(seqLen), int64(e.dim)))
	if err != nil {
		return nil, fmt.Errorf("embed: allocating the output: %w", err)
	}
	defer func() { _ = output.Destroy() }()

	e.mu.Lock()
	err = e.session.Run(inputs, []ort.Value{output})
	e.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("embed: running the model: %w", err)
	}

	return pool(output.GetData(), mask, len(texts), seqLen, e.dim, e.pooling), nil
}

// newInputs builds the three BERT input tensors.
func (e *Encoder) newInputs(shape ort.Shape, ids, mask, types []int64) ([]ort.Value, error) {
	inputs := make([]ort.Value, 0, len(e.options.InputNames))
	for _, data := range [][]int64{ids, mask, types} {
		tensor, err := ort.NewTensor(shape, data)
		if err != nil {
			releaseValues(inputs)
			return nil, fmt.Errorf("embed: building an input tensor: %w", err)
		}
		inputs = append(inputs, tensor)
	}
	return inputs, nil
}

// releaseValues frees tensors, ignoring the errors of an already-failing path.
func releaseValues(values []ort.Value) {
	for _, value := range values {
		if destroyer, ok := value.(interface{ Destroy() error }); ok {
			_ = destroyer.Destroy()
		}
	}
}

// pool turns the token vectors of each text into one vector and normalizes it,
// which is what makes a dot product a cosine. "cls" takes the first token,
// "mean" averages over the real tokens; a model was trained for exactly one of
// them, and using the other quietly loses quality.
func pool(data []float32, mask []int64, batch, tokens, dim int, mode string) [][]float32 {
	if mode == PoolingCLS {
		return poolCLS(data, batch, tokens, dim)
	}
	return meanPool(data, mask, batch, tokens, dim)
}

// poolCLS takes the first token of each sequence, which is what a BERT model
// exports for classification and what BGE retrieval models are trained on.
func poolCLS(data []float32, batch, tokens, dim int) [][]float32 {
	vectors := make([][]float32, batch)
	for i := 0; i < batch; i++ {
		vector := make([]float32, dim)
		copy(vector, data[i*tokens*dim:i*tokens*dim+dim])
		normalizeVecInPlace(vector)
		vectors[i] = vector
	}
	return vectors
}

// meanPool averages each text's token vectors over its real tokens.
func meanPool(data []float32, mask []int64, batch, tokens, dim int) [][]float32 {
	vectors := make([][]float32, batch)
	for i := 0; i < batch; i++ {
		sum := make([]float64, dim)
		count := 0.0
		for t := 0; t < tokens; t++ {
			if mask[i*tokens+t] == 0 {
				continue
			}
			count++
			base := (i*tokens + t) * dim
			for d := 0; d < dim; d++ {
				sum[d] += float64(data[base+d])
			}
		}

		vector := make([]float32, dim)
		if count == 0 {
			vectors[i] = vector
			continue
		}
		for d := range sum {
			vector[d] = float32(sum[d] / count)
		}
		normalizeVecInPlace(vector)
		vectors[i] = vector
	}
	return vectors
}

// normalizeVecInPlace scales a vector to unit length, ignoring an empty one.
func normalizeVecInPlace(vector []float32) {
	var norm float64
	for _, value := range vector {
		norm += float64(value) * float64(value)
	}
	if norm <= 0 {
		return
	}
	scale := math.Sqrt(norm)
	for i := range vector {
		vector[i] = float32(float64(vector[i]) / scale)
	}
}

// onnxFileName returns the model file the manifest lists.
func onnxFileName(manifest Manifest) string {
	for _, file := range manifest.Files {
		if filepath.Ext(file.Name) == ".onnx" {
			return file.Name
		}
	}
	return ""
}
