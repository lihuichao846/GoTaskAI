package kbimport

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHashContent(t *testing.T) {
	if got := HashContent(""); got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("unexpected sha256 for empty content: %s", got)
	}
	if HashContent("hello") != HashContent("hello") {
		t.Fatal("hash should be deterministic for identical content")
	}
	if HashContent("hello") == HashContent("hello ") {
		t.Fatal("hash should differ when content changes")
	}
}

func TestDecide(t *testing.T) {
	cases := []struct {
		name         string
		existingHash string
		newHash      string
		want         Action
	}{
		{"not in library", "", "abc", ActionCreate},
		{"identical content", "abc", "abc", ActionSkip},
		{"content changed", "abc", "def", ActionRebuild},
	}
	for _, c := range cases {
		if got := Decide(c.existingHash, c.newHash); got != c.want {
			t.Fatalf("%s: Decide(%q, %q) = %v, want %v", c.name, c.existingHash, c.newHash, got, c.want)
		}
	}
}

func TestActionString(t *testing.T) {
	for action, want := range map[Action]string{
		ActionCreate:  "create",
		ActionSkip:    "skip",
		ActionRebuild: "rebuild",
	} {
		if got := action.String(); got != want {
			t.Fatalf("Action(%d).String() = %q, want %q", action, got, want)
		}
	}
}

func TestScanDirFiltersAndSorts(t *testing.T) {
	root := t.TempDir()

	writeFile(t, filepath.Join(root, "b.md"), "second")
	writeFile(t, filepath.Join(root, "a.md"), "first")
	writeFile(t, filepath.Join(root, "notes.txt"), "plain text")
	writeFile(t, filepath.Join(root, "image.bin"), "not a text extension")
	writeFile(t, filepath.Join(root, ".hidden.md"), "hidden file should be skipped")
	writeFile(t, filepath.Join(root, "empty.md"), "")

	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir hidden dir: %v", err)
	}
	writeFile(t, filepath.Join(root, ".git", "config.md"), "inside hidden dir")

	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	writeFile(t, filepath.Join(root, "sub", "c.md"), "nested")

	files, err := ScanDir(root, 0)
	if err != nil {
		t.Fatalf("ScanDir error: %v", err)
	}

	// 按绝对路径排序：a.md < b.md < notes.txt < sub/c.md
	wantNames := []string{"a.md", "b.md", "notes.txt", "c.md"}
	if len(files) != len(wantNames) {
		t.Fatalf("expected %d files, got %d: %+v", len(wantNames), len(files), files)
	}
	for i, want := range wantNames {
		if got := filepath.Base(files[i].Path); got != want {
			t.Fatalf("file[%d] = %s, want %s (results must be sorted)", i, got, want)
		}
	}
	// 指纹必须与内容一致，供增量判定使用。
	if files[0].Hash != HashContent("first") {
		t.Fatalf("hash mismatch for a.md: %s", files[0].Hash)
	}
}

func TestScanDirRespectsLimit(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.md", "b.md", "c.md"} {
		writeFile(t, filepath.Join(root, name), name)
	}

	files, err := ScanDir(root, 2)
	if err != nil {
		t.Fatalf("ScanDir error: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected limit to cap results at 2, got %d", len(files))
	}
}

func TestScanDirRejectsNonDirectory(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.md")
	writeFile(t, file, "content")

	if _, err := ScanDir(file, 0); err == nil {
		t.Fatal("expected error when source is not a directory")
	}
}

func TestScanDirMissingSource(t *testing.T) {
	if _, err := ScanDir(filepath.Join(t.TempDir(), "does-not-exist"), 0); err == nil {
		t.Fatal("expected error when source directory is missing")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
