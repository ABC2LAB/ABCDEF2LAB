package pathmask

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var testKey = bytes.Repeat([]byte{7}, KeyBytes)

// 가림 규칙의 합성 벡터는 testdata에 두어 다른 언어의 소비자도 같은 값으로 시험할 수 있게 한다.
func TestMaskPath(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "path-mask-v2.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Profile string `json:"profile"`
		KeyHex  string `json:"key_hex"`
		KeyRef  string `json:"key_ref"`
		Vectors []struct {
			Name           string `json:"name"`
			Path           string `json:"path"`
			PathPattern    string `json:"path_pattern"`
			MaskedSegments int    `json:"masked_segments"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	key, err := hex.DecodeString(file.KeyHex)
	if file.Profile != Profile || len(file.Vectors) < 20 || err != nil || len(key) != KeyBytes {
		t.Fatal(file.Profile, len(file.Vectors), err)
	}
	if got := KeyRef(key); got != file.KeyRef || got == KeyRef(testKey) {
		t.Fatal(got)
	}
	for _, tc := range file.Vectors {
		t.Run(tc.Name, func(t *testing.T) {
			pattern, masked := Mask(tc.Path, key)
			if pattern != tc.PathPattern || masked != tc.MaskedSegments {
				t.Fatalf("got %q %d want %q %d", pattern, masked, tc.PathPattern, tc.MaskedSegments)
			}
		})
	}
}

func TestMaskPathBoundsSegments(t *testing.T) {
	fits := strings.Repeat("/1", MaxSegments) + "/"
	if pattern, masked := Mask(fits, testKey); pattern != strings.Repeat("/{id}", MaxSegments)+"/" || masked != MaxSegments {
		t.Fatal(pattern, masked)
	}
	tooLong := strings.Repeat("/a", MaxSegments+1)
	if pattern, masked := Mask(tooLong, testKey); pattern != LongPath || masked != MaxSegments+1 {
		t.Fatal(pattern, masked)
	}
}
