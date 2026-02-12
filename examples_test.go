package zstd

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
)

func Example_writerReader() {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	_, err := w.Write([]byte("Hello, zstd!"))
	if err != nil {
		log.Fatal(err)
	}
	if err := w.Close(); err != nil {
		log.Fatal(err)
	}

	r, err := NewReader(&buf)
	if err != nil {
		log.Fatal(err)
	}
	if _, err := io.Copy(os.Stdout, r); err != nil {
		log.Fatal(err)
	}
	_ = r.Close()
	// Output:
	// Hello, zstd!
}

func ExampleWriter_SetLevel() {
	data := []byte(strings.Repeat("the quick brown fox jumps over the lazy dog. ", 100))

	compress := func(level int) []byte {
		var buf bytes.Buffer
		w := NewWriter(&buf)
		if err := w.SetLevel(level); err != nil {
			log.Fatal(err)
		}
		w.Reset(&buf)
		if _, err := w.Write(data); err != nil {
			log.Fatal(err)
		}
		if err := w.Close(); err != nil {
			log.Fatal(err)
		}
		return buf.Bytes()
	}

	fast := compress(BestSpeed)
	best := compress(BestCompression)

	decompress := func(compressed []byte) []byte {
		r, err := NewReader(bytes.NewReader(compressed))
		if err != nil {
			log.Fatal(err)
		}
		defer r.Close()
		dec, err := io.ReadAll(r)
		if err != nil {
			log.Fatal(err)
		}
		return dec
	}

	if string(decompress(fast)) != string(data) {
		log.Fatal("BestSpeed mismatch")
	}
	fmt.Println("BestSpeed: OK")

	if string(decompress(best)) != string(data) {
		log.Fatal("BestCompression mismatch")
	}
	fmt.Println("BestCompression: OK")
	fmt.Println("BestCompression <= BestSpeed:", len(best) <= len(fast))
	// Output:
	// BestSpeed: OK
	// BestCompression: OK
	// BestCompression <= BestSpeed: true
}

func Example_reset() {
	proverbs := []string{
		"Don't communicate by sharing memory, share memory by communicating.",
		"Concurrency is not parallelism.",
		"The bigger the interface, the weaker the abstraction.",
		"Documentation is for users.",
	}

	var buf bytes.Buffer
	w := NewWriter(&buf)
	r, err := NewReader(&buf)
	if err != nil {
		log.Fatal(err)
	}

	for _, p := range proverbs {
		buf.Reset()
		w.Reset(&buf)

		if _, err := w.Write([]byte(p)); err != nil {
			log.Fatal(err)
		}
		if err := w.Close(); err != nil {
			log.Fatal(err)
		}

		if err := r.Reset(&buf); err != nil {
			log.Fatal(err)
		}
		if _, err := io.Copy(os.Stdout, r); err != nil {
			log.Fatal(err)
		}
		fmt.Println()
	}
	r.Close()
	// Output:
	// Don't communicate by sharing memory, share memory by communicating.
	// Concurrency is not parallelism.
	// The bigger the interface, the weaker the abstraction.
	// Documentation is for users.
}

func ExampleWriter_Flush() {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	if _, err := w.Write([]byte("first part.")); err != nil {
		log.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		log.Fatal(err)
	}

	if _, err := w.Write([]byte("second part.")); err != nil {
		log.Fatal(err)
	}
	if err := w.Close(); err != nil {
		log.Fatal(err)
	}

	r, err := NewReader(&buf)
	if err != nil {
		log.Fatal(err)
	}
	defer r.Close()
	if _, err := io.Copy(os.Stdout, r); err != nil {
		log.Fatal(err)
	}
	// Output:
	// first part.second part.
}

func ExampleWriter_ReadFrom() {
	input := strings.NewReader("ReadFrom compresses data from an io.Reader efficiently.")
	var buf bytes.Buffer
	w := NewWriter(&buf)

	if _, err := w.ReadFrom(input); err != nil {
		log.Fatal(err)
	}
	if err := w.Close(); err != nil {
		log.Fatal(err)
	}

	r, err := NewReader(&buf)
	if err != nil {
		log.Fatal(err)
	}
	defer r.Close()
	if _, err := io.Copy(os.Stdout, r); err != nil {
		log.Fatal(err)
	}
	// Output:
	// ReadFrom compresses data from an io.Reader efficiently.
}

func ExampleWriter_SetRawDict() {
	dict := []byte("the quick brown fox jumps over the lazy dog")
	data := []byte("the quick brown fox leaps over the sleepy dog")

	compressWithDict := func(d []byte) []byte {
		var buf bytes.Buffer
		w := NewWriter(&buf)
		if d != nil {
			w.SetRawDict(d)
		}
		w.Reset(&buf)
		if _, err := w.Write(data); err != nil {
			log.Fatal(err)
		}
		if err := w.Close(); err != nil {
			log.Fatal(err)
		}
		return buf.Bytes()
	}

	without := compressWithDict(nil)
	with := compressWithDict(dict)

	decompress := func(compressed []byte, d []byte) []byte {
		r, err := NewReader(bytes.NewReader(compressed))
		if err != nil {
			log.Fatal(err)
		}
		defer r.Close()
		if d != nil {
			r.SetRawDict(d)
		}
		dec, err := io.ReadAll(r)
		if err != nil {
			log.Fatal(err)
		}
		return dec
	}

	if string(decompress(without, nil)) != string(data) {
		log.Fatal("without dict mismatch")
	}
	fmt.Println("without dict: OK")

	if string(decompress(with, dict)) != string(data) {
		log.Fatal("with dict mismatch")
	}
	fmt.Println("with dict: OK")
	fmt.Println("dict smaller:", len(with) < len(without))
	// Output:
	// without dict: OK
	// with dict: OK
	// dict smaller: true
}
