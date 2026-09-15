package processlog

import (
	"bytes"
	"reflect"
	"testing"
)

func TestWriterEmitsCompleteLinesAndFlushesTail(t *testing.T) {
	var got []string
	w := New(func(line string) { got = append(got, line) })

	for _, chunk := range []string{"first\r", "\nsec", "ond\nthird"} {
		if n, err := w.Write([]byte(chunk)); err != nil || n != len(chunk) {
			t.Fatalf("Write(%q) = (%d, %v), want (%d, nil)", chunk, n, err, len(chunk))
		}
	}
	if want := []string{"first", "second"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("before Flush lines = %#v, want %#v", got, want)
	}

	w.Flush()
	w.Flush()
	if want := []string{"first", "second", "third"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after Flush lines = %#v, want %#v", got, want)
	}
}

func TestWriterBoundsAPathologicalLine(t *testing.T) {
	var lengths []int
	w := New(func(line string) { lengths = append(lengths, len(line)) })
	payload := append(bytes.Repeat([]byte{'x'}, maxPendingBytes+7), '\n')

	if _, err := w.Write(payload); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if want := []int{maxPendingBytes, 7}; !reflect.DeepEqual(lengths, want) {
		t.Fatalf("emitted lengths = %v, want bounded chunks %v", lengths, want)
	}
}

func TestWriterDoesNotInventEmptyLineAfterExactBoundary(t *testing.T) {
	var lengths []int
	w := New(func(line string) { lengths = append(lengths, len(line)) })

	if _, err := w.Write(bytes.Repeat([]byte{'x'}, maxPendingBytes)); err != nil {
		t.Fatalf("write boundary-sized line: %v", err)
	}
	if _, err := w.Write([]byte{'\n'}); err != nil {
		t.Fatalf("write terminating newline: %v", err)
	}

	if want := []int{maxPendingBytes}; !reflect.DeepEqual(lengths, want) {
		t.Fatalf("emitted lengths = %v, want %v", lengths, want)
	}
}
