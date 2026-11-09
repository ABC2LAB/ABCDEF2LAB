package diagnosis

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"sync"
)

// decodeFrame은 알 수 없는 필드·중복 키·추가 JSON을 거부한다.
func decodeFrame[T any](raw []byte, value *T) error {
	if len(raw) == 0 || len(raw) > MaxFrameBytes {
		return ErrProtocol
	}
	if !closedJSONShape(raw, reflect.TypeOf(value).Elem()) {
		return ErrProtocol
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil {
		return ErrProtocol
	}
	return nil
}

// Serve는 운영자가 연결한 비공개 상속 pipe만 받는다. TCP·브라우저는 받지 않는다.
func Serve(ctx context.Context, input io.Reader, output io.Writer, gateway Gateway) error {
	if ctx == nil || input == nil || output == nil || gateway == nil {
		return ErrProtocol
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), MaxFrameBytes+1)
	encoder := json.NewEncoder(output)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return ErrUnavailable
		}
		var request Request
		if decodeFrame(scanner.Bytes(), &request) != nil {
			return ErrProtocol
		}
		response, err := gateway.Call(ctx, request)
		if err != nil {
			return ErrUnavailable
		}
		if encoder.Encode(response) != nil {
			return ErrUnavailable
		}
	}
	if scanner.Err() != nil {
		return ErrProtocol
	}
	return nil
}

type Client struct {
	mu     sync.Mutex
	reader *bufio.Reader
	writer io.Writer
	close  func() error
	broken bool
}

func NewClient(input io.Reader, output io.Writer, closeTransport func() error) (*Client, error) {
	if input == nil || output == nil || closeTransport == nil {
		return nil, ErrProtocol
	}
	return &Client{reader: bufio.NewReaderSize(input, MaxFrameBytes+1), writer: output, close: closeTransport}, nil
}

// Call은 한 요청만 보낸다. 응답 불명·취소 뒤에는 pipe를 폐기하며 Start를 재전송하지 않는다.
func (c *Client) Call(ctx context.Context, request Request) (Response, error) {
	if ctx == nil || ctx.Err() != nil || !validRequest(request) {
		return Response{}, ErrProtocol
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if ctx.Err() != nil {
		return Response{}, ErrUnavailable
	}
	if c.broken {
		return Response{}, ErrUnavailable
	}
	stop := context.AfterFunc(ctx, func() { _ = c.close() })
	defer stop()
	fail := func() (Response, error) { c.broken = true; _ = c.close(); return Response{}, ErrUnavailable }
	raw, err := json.Marshal(request)
	if err != nil {
		return Response{}, ErrProtocol
	}
	raw = append(raw, '\n')
	if n, err := c.writer.Write(raw); err != nil || n != len(raw) {
		return fail()
	}
	frame, err := c.reader.ReadSlice('\n')
	if err != nil || ctx.Err() != nil {
		return fail()
	}
	var response Response
	if decodeFrame(frame, &response) != nil || response.Version != Version || response.RequestID != request.RequestID {
		return fail()
	}
	if response.Snapshot != nil && (response.Snapshot.TaskID != request.TaskID || response.Snapshot.PreparationRef != request.PreparationRef) {
		return fail()
	}
	if response.Snapshot != nil && !validSnapshot(*response.Snapshot) {
		return fail()
	}
	if response.Error != "" && !validError(response.Error) {
		return fail()
	}
	if (response.Snapshot == nil) == (response.Error == "") {
		return fail()
	}
	return response, nil
}

// encoding/json의 대소문자 별칭과 중첩 중복 필드도 계약에 포함하지 않는다.
func closedJSONShape(raw []byte, typ reflect.Type) bool {
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return true
	}
	tokens := json.NewDecoder(bytes.NewReader(raw))
	if token, err := tokens.Token(); err != nil || token != json.Delim('{') {
		return false
	}
	fields := map[string]reflect.Type{}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			fields[name] = field.Type
		}
	}
	seen := map[string]bool{}
	for tokens.More() {
		token, err := tokens.Token()
		name, ok := token.(string)
		field, known := fields[name]
		if err != nil || !ok || !known || seen[name] {
			return false
		}
		seen[name] = true
		var value json.RawMessage
		if tokens.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) || !closedJSONShape(value, field) {
			return false
		}
	}
	if _, err := tokens.Token(); err != nil {
		return false
	}
	_, err := tokens.Token()
	return err == io.EOF
}
