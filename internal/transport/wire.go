package transport

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const MaxFrameBytes = 8 * 1024 * 1024

func Decode(data []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("expected exactly one JSON value")
	}
	return nil
}

func readFrame(r io.Reader, out any) error {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > MaxFrameBytes {
		return fmt.Errorf("invalid frame length %d (maximum %d)", size, MaxFrameBytes)
	}
	b := make([]byte, int(size))
	if _, err := io.ReadFull(r, b); err != nil {
		return err
	}
	return Decode(b, out)
}

func writeFrame(w io.Writer, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(b) > MaxFrameBytes {
		return errors.New("response/request exceeds IPC frame limit")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(b)))
	if err := writeAll(w, header[:]); err != nil {
		return err
	}
	return writeAll(w, b)
}

func writeAll(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}

func Reply(req Request, payload any, err *Error) Response {
	var data json.RawMessage
	if payload != nil {
		b, encodeErr := json.Marshal(payload)
		if encodeErr != nil {
			err = &Error{Code: CodeRuntime, Message: "encode response failed"}
		} else {
			data = b
		}
	}
	return Response{Version: ProtocolVersion, ID: req.ID, Payload: data, Error: err}
}
