package project

import "testing"

func TestIsTestFile(t *testing.T) {
	for _, p := range []string{
		"x_test.go", "pkg/x_test.go", "test_x.py", "pkg/test_x.py", "x_test.py", "a.spec.ts", "a.test.js", "a.test.d.ts",
		"x_spec.rb", "testdata/x.go", "pkg/testdata/x.go", "pkg/tests/x.go", "test/x.c", "spec/models/user.rb",
		"web/__tests__/app.jsx", "conftest.py", "src/conftest.py", "src/TestFoo.java",
	} {
		if !IsTestFile(p) {
			t.Errorf("%s should be a test file", p)
		}
	}
	for _, p := range []string{"", "internal/x/x.go", "contest/x.go", "latest.go", "attest_x.py"} {
		if IsTestFile(p) {
			t.Errorf("%s is not a test file", p)
		}
	}
}
