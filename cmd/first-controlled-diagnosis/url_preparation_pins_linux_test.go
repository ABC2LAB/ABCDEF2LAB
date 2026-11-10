//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestURLPreparationHeldPinMutation(t *testing.T) {
	for _, change := range []string{"content_restore_mtime", "replace", "file_mode", "parent_mode", "ancestor_replace", "hardlink", "released"} {
		t.Run(change, func(t *testing.T) {
			root := t.TempDir()
			if os.Chmod(root, 0700) != nil {
				t.Fatal("chmod")
			}
			dir := filepath.Join(root, "private")
			if os.Mkdir(dir, 0700) != nil {
				t.Fatal("mkdir")
			}
			path := filepath.Join(dir, "pin")
			if os.WriteFile(path, []byte("original"), 0600) != nil {
				t.Fatal("write")
			}
			_, pin, err := urlReadPin(path, 64, false)
			if err != nil {
				t.Fatal(err)
			}
			held, err := urlOpenHeldInput(pin)
			if err != nil {
				t.Fatal(err)
			}
			defer held.close()
			if held.check() != nil {
				t.Fatal("initial check")
			}
			switch change {
			case "content_restore_mtime":
				time.Sleep(time.Millisecond)
				if os.WriteFile(path, []byte("modified"), 0600) != nil || os.Chtimes(path, pin.info.ModTime(), pin.info.ModTime()) != nil {
					t.Fatal("mutation")
				}
			case "replace":
				if os.Rename(path, path+".old") != nil || os.WriteFile(path, []byte("original"), 0600) != nil {
					t.Fatal("replacement")
				}
			case "file_mode":
				if os.Chmod(path, 0644) != nil {
					t.Fatal("chmod")
				}
			case "parent_mode":
				if os.Chmod(dir, 0755) != nil {
					t.Fatal("chmod")
				}
			case "ancestor_replace":
				if os.Rename(dir, dir+".old") != nil || os.Symlink(dir+".old", dir) != nil {
					t.Fatal("ancestor")
				}
			case "hardlink":
				if os.Link(path, path+".link") != nil {
					t.Fatal("link")
				}
			case "released":
				h := &urlHeldInputs{files: []urlHeldInput{held}}
				h.close()
				held = urlHeldInput{}
				if h.check() == nil {
					t.Fatal("closed accepted")
				}
				return
			}
			if held.check() == nil {
				t.Fatal("changed authority accepted")
			}
		})
	}
}
