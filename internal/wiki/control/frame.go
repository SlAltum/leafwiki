package control

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// MaxFrameBytes is the largest frame a client may send (FR-107).
const MaxFrameBytes = 8 << 20

// decodeSlack is how far past one frame the decoder may read ahead. A decoder
// buffers what it reads, so the next frame's first bytes can already be in it;
// the exact frame size is checked against the decoder's own offset.
const decodeSlack = 64 << 10

// Request is one JSON-RPC 2.0 request.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is one JSON-RPC 2.0 response. Exactly one of Result and Error is set.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// errTooLarge says the peer sent more than one frame is allowed to hold.
var errTooLarge = errors.New("control: frame too large")

// frameReader stops after limit bytes, so a peer cannot make the decoder read
// without bound.
type frameReader struct {
	reader io.Reader
	left   int
}

func (f *frameReader) Read(p []byte) (int, error) {
	if f.left <= 0 {
		return 0, errTooLarge
	}
	if len(p) > f.left {
		p = p[:f.left]
	}
	n, err := f.reader.Read(p)
	f.left -= n
	return n, err
}

// frameStream decodes one request after another from a connection.
//
// One decoder serves the whole connection on purpose: a decoder reads ahead, so
// building a new one per frame would swallow the start of the next frame and lose
// it (a client that pipelines two requests gets one answer and a stall).
type frameStream struct {
	frame   *frameReader
	decoder *json.Decoder
}

// newFrameStream wraps a connection.
func newFrameStream(conn io.Reader) *frameStream {
	frame := &frameReader{reader: bufio.NewReaderSize(conn, 32<<10), left: MaxFrameBytes + decodeSlack}
	return &frameStream{frame: frame, decoder: json.NewDecoder(frame)}
}

// readRequest decodes one request, refusing anything over the frame limit. It
// reports io.EOF when the peer closed cleanly and errIdle when the read deadline
// expired, both without a protocol error.
func (s *frameStream) readRequest() (*Request, error) {
	s.frame.left = MaxFrameBytes + decodeSlack

	before := s.decoder.InputOffset()
	var request Request
	if err := s.decoder.Decode(&request); err != nil {
		if errors.Is(err, errTooLarge) {
			return nil, errTooLarge
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			// A closed connection is not a protocol error.
			return nil, io.EOF
		}
		var netErr interface{ Timeout() bool }
		if errors.As(err, &netErr) && netErr.Timeout() {
			return nil, errIdle
		}
		return nil, fmt.Errorf("decode request: %w", err)
	}

	// The decoder's offset is where the value ended, so this is the frame size,
	// without the bytes it read ahead for the frame after it.
	if size := s.decoder.InputOffset() - before; size > MaxFrameBytes {
		return nil, errTooLarge
	}
	return &request, nil
}

// writeResponse sends one response.
func writeResponse(w io.Writer, response *Response) error {
	response.JSONRPC = "2.0"
	encoder := json.NewEncoder(w)
	return encoder.Encode(response)
}

// resultResponse builds a success response for the request id.
func resultResponse(id json.RawMessage, result any) *Response {
	return &Response{JSONRPC: "2.0", ID: id, Result: result}
}

// errorResponse builds a failure response for the request id.
func errorResponse(id json.RawMessage, err *Error) *Response {
	return &Response{JSONRPC: "2.0", ID: id, Error: err}
}
