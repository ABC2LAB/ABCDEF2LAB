//go:build linux

package main

import (
	"bytes"
	"os"
	"path/filepath"

	"github.com/Autumn-27/artex/server"
)

// 각 쓰기의 실패를 보존한다. 이후 파일 읽기가 이전 쓰기 오류를 덮지 않는다.
type productPinWriter struct {
	dir string
	err error
}

func (p *productPinWriter) write(name string, raw []byte) server.E2HTTPFilePin {
	if p.err == nil {
		p.err = productWrite(filepath.Join(p.dir, name), raw)
	}
	return server.E2HTTPFilePin{File: name, SHA256: urlSHA(raw)}
}

func productAPIModelLaunches(c urlPreparationInstallation) []urlPreparationProfile {
	return productModels(c)
}

// API에는 필요한 설정·JWT·제한된 DB 자격만 복사한다. 실행 비밀과 저널은 투영하지 않는다.
// 원본의 논리 경로를 유지하므로 API도 같은 설치 digest를 검증한다.
func productPrepareAPIProjection(root string) error {
	c, _, err := readURLInstallation(filepath.Join(root, "config", "installation.json"))
	if err != nil {
		return errPreparation
	}
	files := map[string][]byte{}
	defer func() {
		for _, raw := range files {
			clear(raw)
		}
	}()
	add := func(relative, digest string) error {
		raw, _, e := urlReadPin(filepath.Join(root, relative), 256<<10, false)
		if e != nil || digest != "" && urlSHA(raw) != digest {
			clear(raw)
			return errPreparation
		}
		files[relative] = raw
		return nil
	}
	for _, name := range []string{"config/installation.json", "config/api.dsn", "config/model-writer.dsn", "keys/jwt.key"} {
		if add(name, "") != nil {
			return errPreparation
		}
	}
	launches := []urlProtectedPin{c.APILaunch}
	for _, profile := range productAPIModelLaunches(c) {
		launches = append(launches, profile.Launch)
	}
	for _, pin := range launches {
		if filepath.Dir(pin.File) != filepath.Join(root, "config") {
			return errPreparation
		}
		relative := filepath.Join("config", filepath.Base(pin.File))
		if add(relative, pin.SHA256) != nil {
			return errPreparation
		}
		launch, e := parseLaunchConfig(files[relative])
		if e != nil || launch.KeyDir != filepath.Join(root, "keys") || launch.DataDir != filepath.Join(root, "data") {
			return errPreparation
		}
	}
	return productPublishAPIProjection(root, files)
}

func productPublishAPIProjection(root string, files map[string][]byte) error {
	destination := filepath.Join(root, "api")
	if _, err := os.Lstat(destination); err == nil {
		if checkDirectory(destination, true) != nil {
			return errPreparation
		}
		count := 0
		err = filepath.WalkDir(destination, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return errPreparation
			}
			if entry.IsDir() {
				return checkDirectory(path, true)
			}
			rel, e := filepath.Rel(destination, path)
			want, ok := files[rel]
			if e != nil || !ok {
				return errPreparation
			}
			raw, e := readPrivateFile(path, 256<<10)
			defer clear(raw)
			if e != nil || !bytes.Equal(raw, want) {
				return errPreparation
			}
			count++
			return nil
		})
		if err != nil || count != len(files) {
			return errPreparation
		}
		return nil
	} else if !os.IsNotExist(err) {
		return errPreparation
	}
	stage, err := os.MkdirTemp(root, ".api-")
	if err != nil {
		return errPreparation
	}
	defer os.RemoveAll(stage)
	for _, name := range []string{"config", "keys", "data", "state", "runs", "ipc"} {
		if os.Mkdir(filepath.Join(stage, name), 0700) != nil {
			return errPreparation
		}
	}
	for name, raw := range files {
		if productWrite(filepath.Join(stage, name), raw) != nil {
			return errPreparation
		}
	}
	for _, name := range []string{"config", "keys", "."} {
		if syncProductDirectory(filepath.Join(stage, name)) != nil {
			return errPreparation
		}
	}
	if os.Rename(stage, destination) != nil || syncProductDirectory(root) != nil {
		return errPreparation
	}
	return nil
}

func syncProductDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return errPreparation
	}
	defer f.Close()
	return f.Sync()
}
