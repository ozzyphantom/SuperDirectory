package wizard

import "testing"

func TestOverlaps(t *testing.T) {
	cases := []struct {
		name   string
		a, b   string
		expect bool
	}{
		{"identical", "/a/b", "/a/b", true},
		{"child under parent", "/a/b/c", "/a/b", true},
		{"parent contains child", "/a/b", "/a/b/c", true},
		{"siblings", "/a/b", "/a/c", false},
		{"disjoint", "/x/y", "/p/q", false},

		// The sensible default (save alongside as <name>-super) must NOT be
		// blocked — "-super" is a sibling, not a containment.
		{"default suffix sibling", "/u/o/photos-super", "/u/o/photos", false},

		// Case-insensitive volumes (APFS/NTFS): same dir, different case.
		{"case-only same dir", "/u/o/photos", "/u/o/Photos", true},
		{"case-only child", "/u/o/Photos/sub", "/u/o/photos", true},

		// Root as source: every child is inside it.
		{"child of root", "/foo-super", "/", true},
		{"root and root", "/", "/", true},
	}
	for _, c := range cases {
		if got := overlaps(c.a, c.b); got != c.expect {
			t.Errorf("%s: overlaps(%q,%q)=%v, want %v", c.name, c.a, c.b, got, c.expect)
		}
	}
}

func TestSafeBase(t *testing.T) {
	cases := map[string]string{
		"/a/b":    "b",
		"/":       "flattened",
		"/photos": "photos",
		"/a/b/":   "b",
	}
	for in, want := range cases {
		if got := safeBase(in); got != want {
			t.Errorf("safeBase(%q)=%q, want %q", in, got, want)
		}
	}
}
