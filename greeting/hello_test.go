package greeting

import (
	"testing"
)

func TestHello(t *testing.T) {
	hello := Hello("gopher")
	expected := "Hello gopher!"
	if hello != expected {
		t.Errorf("Expected %s, got %s", expected, hello)
	}
}
