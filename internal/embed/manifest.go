// Package embed describes and verifies the encoder model a LeafWiki instance
// uses for semantic search. It deliberately knows nothing about the inference
// runtime: the model directory is data, and the encoder that consumes it lives
// behind a build tag (see docs/srs/zh-search-and-retrieval.srs.md FR-402).
package embed

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var (
	// ErrModelUnavailable means the models directory holds no usable model, so
	// semantic search cannot run. Keyword search is unaffected (FR-402).
	ErrModelUnavailable = errors.New("embed: no model with a manifest")
	// ErrModelAmbiguous means more than one model directory was found; picking
	// one at random would make results depend on directory listing order.
	ErrModelAmbiguous = errors.New("embed: more than one model directory")
	// ErrInvalidManifest means the manifest is malformed or incomplete.
	ErrInvalidManifest = errors.New("embed: invalid manifest")
	// ErrChecksumMismatch means a model file does not match the digest its
	// manifest recorded, so the files are not the ones the model was built
	// from (FR-403).
	ErrChecksumMismatch = errors.New("embed: file does not match its manifest checksum")
)

// ManifestJSON is the file name of the manifest inside a model directory.
const ManifestJSON = "manifest.json"

// supportedDTypes are the storage formats the encoder can decode.
var supportedDTypes = map[string]bool{
	"float32": true,
	"float16": true,
}

// File is one model artifact with the digest recorded when the model was
// packaged.
type File struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

// Manifest describes a packaged model (docs/srs/zh-search-and-retrieval.srs.md
// §6.6).
// Pooling modes a model can be exported with.
const (
	PoolingMean = "mean"
	PoolingCLS  = "cls"
)

type Manifest struct {
	ModelID   string `json:"model_id"`
	Dim       int    `json:"dim"`
	DType     string `json:"dtype"`
	Tokenizer string `json:"tokenizer"`
	MaxTokens int    `json:"max_tokens"`
	// Pooling is how token vectors become one vector: "mean" averages over the
	// real tokens, "cls" takes the classification token. Models are trained
	// with one of them, and using the other quietly loses quality - a BGE model
	// is trained for CLS, a sentence-transformer for mean.
	Pooling string `json:"pooling"`
	// QueryPrefix, when set, is prepended to a query before encoding (BGE's
	// Chinese retrieval models ask for an instruction on the query side only).
	QueryPrefix string `json:"query_prefix"`
	Files       []File `json:"files"`
}

// Model is a verified-on-demand model directory.
type Model struct {
	// Dir is the directory holding the manifest and its files.
	Dir string
	// Manifest is the parsed manifest inside Dir.
	Manifest Manifest
}

// Open finds the model in modelsDir. modelsDir is the parent of the model
// directories (<data-dir>/models by default), not a model directory itself.
func Open(modelsDir string) (*Model, error) {
	// A directory that holds the manifest itself is a model directory: pointing
	// the option at the model directly is the obvious reading of
	// --vectors-model-dir, and having several models side by side is normal.
	if _, err := os.Stat(filepath.Join(modelsDir, ManifestJSON)); err == nil {
		return openDir(modelsDir)
	}

	entries, err := os.ReadDir(modelsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s does not exist", ErrModelUnavailable, modelsDir)
		}
		return nil, err
	}

	var candidates []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(modelsDir, entry.Name())
		if _, err := os.Stat(filepath.Join(dir, ManifestJSON)); err == nil {
			candidates = append(candidates, dir)
		}
	}

	switch len(candidates) {
	case 0:
		return nil, fmt.Errorf("%w: no subdirectory of %s holds a %s", ErrModelUnavailable, modelsDir, ManifestJSON)
	case 1:
		return openDir(candidates[0])
	default:
		sort.Strings(candidates)
		return nil, fmt.Errorf("%w: %s", ErrModelAmbiguous, strings.Join(candidates, ", "))
	}
}

// openDir reads and validates the manifest of one model directory.
func openDir(dir string) (*Model, error) {
	raw, err := os.ReadFile(filepath.Join(dir, ManifestJSON))
	if err != nil {
		return nil, err
	}

	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrInvalidManifest, dir, err)
	}
	if err := manifest.validate(); err != nil {
		return nil, err
	}

	return &Model{Dir: dir, Manifest: manifest}, nil
}

// validate reports the first field that makes the manifest unusable.
func (m Manifest) validate() error {
	if strings.TrimSpace(m.ModelID) == "" {
		return fmt.Errorf("%w: model_id is empty", ErrInvalidManifest)
	}
	if m.Dim <= 0 {
		return fmt.Errorf("%w: dim must be positive, got %d", ErrInvalidManifest, m.Dim)
	}
	if !supportedDTypes[m.DType] {
		return fmt.Errorf("%w: unsupported dtype %q", ErrInvalidManifest, m.DType)
	}
	if strings.TrimSpace(m.Tokenizer) == "" {
		return fmt.Errorf("%w: tokenizer is empty", ErrInvalidManifest)
	}
	if m.MaxTokens < 0 {
		return fmt.Errorf("%w: max_tokens must not be negative, got %d", ErrInvalidManifest, m.MaxTokens)
	}
	if m.Pooling == "" {
		// Older manifests predate the field; a sentence-transformer export is
		// the mean-pooling shape.
		m.Pooling = PoolingMean
	}
	if m.Pooling != PoolingMean && m.Pooling != PoolingCLS {
		return fmt.Errorf("%w: unsupported pooling %q", ErrInvalidManifest, m.Pooling)
	}
	if len(m.Files) == 0 {
		return fmt.Errorf("%w: files is empty", ErrInvalidManifest)
	}

	seen := make(map[string]struct{}, len(m.Files))
	for _, file := range m.Files {
		if err := file.validate(); err != nil {
			return err
		}
		if _, duplicate := seen[file.Name]; duplicate {
			return fmt.Errorf("%w: file %q is listed twice", ErrInvalidManifest, file.Name)
		}
		seen[file.Name] = struct{}{}
	}
	return nil
}

// validate rejects a file entry that could escape the model directory or that
// carries a digest the verifier cannot compare against.
func (f File) validate() error {
	name := strings.TrimSpace(f.Name)
	if name == "" {
		return fmt.Errorf("%w: a file entry has no name", ErrInvalidManifest)
	}
	if name != filepath.Base(name) || name == "." || name == ".." {
		return fmt.Errorf("%w: file %q is not a plain file name", ErrInvalidManifest, f.Name)
	}
	if len(f.SHA256) != sha256.Size*2 {
		return fmt.Errorf("%w: file %q has a malformed sha256", ErrInvalidManifest, f.Name)
	}
	if _, err := hex.DecodeString(f.SHA256); err != nil {
		return fmt.Errorf("%w: file %q has a malformed sha256", ErrInvalidManifest, f.Name)
	}
	return nil
}

// Verify checks every file against the digest its manifest recorded. It runs
// before the encoder is loaded, so a model that was copied incompletely or
// replaced behind its manifest is refused instead of producing nonsense
// vectors (FR-403).
func (m *Model) Verify() error {
	for _, file := range m.Manifest.Files {
		sum, err := fileSHA256(filepath.Join(m.Dir, file.Name))
		if err != nil {
			return err
		}
		if !strings.EqualFold(sum, file.SHA256) {
			return fmt.Errorf("%w: %s", ErrChecksumMismatch, file.Name)
		}
	}
	return nil
}

// fileSHA256 returns the hex digest of one file.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = f.Close()
	}()

	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
