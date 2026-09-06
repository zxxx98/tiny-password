package archive

import (
	"strings"
	"testing"
)

func TestValidateEntriesRejectsUnsafePaths(t *testing.T) {
	cases := []struct {
		name string
		path string
	}{
		{"absolute", "/etc/passwd"},
		{"parent traversal", "../evil.txt"},
		{"inner traversal", "a/../../evil.txt"},
		{"bare dotdot", ".."},
		{"windows separator", `a\..\evil.txt`},
		{"empty", ""},
		{"nested 7z", "nested/archive.7z"},
		{"nested zip", "nested/archive.ZIP"},
		{"nested tar.gz", "nested/archive.tar.gz"},
		{"symlink", "link"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isLink := tc.name == "symlink"
			entries := []Entry{{Path: tc.path, Size: 1, Attributes: mapAttrs(isLink)}}
			if err := ValidateEntries(entries); err == nil {
				t.Fatalf("accepted unsafe entry %q", tc.path)
			}
		})
	}
}

func mapAttrs(isLink bool) string {
	if isLink {
		return "lnx"
	}
	return "-rw-r--r--"
}

func TestValidateEntriesRejectsDuplicatesAndLimits(t *testing.T) {
	dup := []Entry{
		{Path: "a.txt", Size: 1},
		{Path: "a.txt", Size: 1},
	}
	if err := ValidateEntries(dup); err == nil {
		t.Fatal("accepted duplicate entry")
	}
	over := []Entry{{Path: "big.bin", Size: MaxExtractBytes + 1}}
	if err := ValidateEntries(over); err == nil {
		t.Fatal("accepted oversized total")
	}
	many := make([]Entry, 0, MaxFiles+1)
	for i := 0; i <= MaxFiles; i++ {
		many = append(many, Entry{Path: strings.Repeat("d", 1) + string(rune('a'+i%26)) + "/" + strings.Repeat("f", i%7+1) + itoa(i), Size: 1})
	}
	if err := ValidateEntries(many); err == nil {
		t.Fatal("accepted too many files")
	}
	empty := []Entry{}
	if err := ValidateEntries(empty); err == nil {
		t.Fatal("accepted empty archive")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestValidateEntriesAcceptsCleanSet(t *testing.T) {
	entries := []Entry{
		{Path: "manifest.json", Size: 256},
		{Path: "items/login-1.json", Size: 1024},
		{Path: "items/notes/note-1.json", Size: 2048},
	}
	if err := ValidateEntries(entries); err != nil {
		t.Fatalf("clean set rejected: %v", err)
	}
}

func TestParseSLT(t *testing.T) {
	out := `
7-Zip (z) 26.03 (arm64): listing archive: /tmp/a.7z

--
Path = manifest.json
Size = 256
Attributes = -rw-r--r--

Path = items
Size = 0
Attributes = D_ drwxr-xr-x

Path = items/login-1.json
Size = 1024
Attributes = -rw-r--r--
`
	entries := parseSLT(out)
	if len(entries) != 3 {
		t.Fatalf("entries=%d, want 3", len(entries))
	}
	if entries[0].Path != "manifest.json" || entries[0].Size != 256 || entries[0].IsDir {
		t.Fatalf("entry 0: %+v", entries[0])
	}
	if !entries[1].IsDir {
		t.Fatal("directory not detected")
	}
}
