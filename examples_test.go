package zstd_test

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/klauspost/stdgozstd"
)

func Example_writerReader() {
	var buf bytes.Buffer
	w, err := zstd.NewWriter(&buf)
	if err != nil {
		log.Fatal(err)
	}
	if _, err := w.Write([]byte("Hello, zstd!")); err != nil {
		log.Fatal(err)
	}
	if err := w.Close(); err != nil {
		log.Fatal(err)
	}

	r, err := zstd.NewReader(&buf)
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

func ExampleEncoder_AppendCompress() {
	src := []byte("One-shot compression is the simplest API.")

	e, err := zstd.NewEncoder()
	if err != nil {
		log.Fatal(err)
	}
	compressed := e.AppendCompress(nil, src)

	d, err := zstd.NewDecoder()
	if err != nil {
		log.Fatal(err)
	}
	decompressed, err := d.AppendDecompress(nil, compressed)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(decompressed))
	// Output:
	// One-shot compression is the simplest API.
}

func ExampleDecoder_AppendDecompress() {
	src := []byte("appended to existing buffer")

	e, err := zstd.NewEncoder()
	if err != nil {
		log.Fatal(err)
	}
	compressed := e.AppendCompress(nil, src)

	d, err := zstd.NewDecoder()
	if err != nil {
		log.Fatal(err)
	}

	prefix := []byte("data: ")
	result, err := d.AppendDecompress(prefix, compressed)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(result))
	// Output:
	// data: appended to existing buffer
}

func ExampleWithEncoderLevel() {
	data := []byte(strings.Repeat("the quick brown fox jumps over the lazy dog. ", 100))

	compress := func(level int) []byte {
		e, err := zstd.NewEncoder(zstd.WithEncoderLevel(level))
		if err != nil {
			log.Fatal(err)
		}
		return e.AppendCompress(nil, data)
	}

	fast := compress(zstd.BestSpeed)
	best := compress(zstd.BestCompression)

	d, err := zstd.NewDecoder()
	if err != nil {
		log.Fatal(err)
	}

	got, err := d.AppendDecompress(nil, fast)
	if err != nil || string(got) != string(data) {
		log.Fatal("BestSpeed mismatch")
	}
	fmt.Println("BestSpeed: OK")

	got, err = d.AppendDecompress(nil, best)
	if err != nil || string(got) != string(data) {
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
	w, err := zstd.NewWriter(&buf)
	if err != nil {
		log.Fatal(err)
	}
	r, err := zstd.NewReader(&buf)
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
	w, err := zstd.NewWriter(&buf)
	if err != nil {
		log.Fatal(err)
	}

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

	r, err := zstd.NewReader(&buf)
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
	w, err := zstd.NewWriter(&buf)
	if err != nil {
		log.Fatal(err)
	}

	if _, err := w.ReadFrom(input); err != nil {
		log.Fatal(err)
	}
	if err := w.Close(); err != nil {
		log.Fatal(err)
	}

	r, err := zstd.NewReader(&buf)
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

func ExampleWithEncoderRawDict() {
	dict := []byte("the quick brown fox jumps over the lazy dog")
	data := []byte("the quick brown fox leaps over the sleepy dog")

	compressWithDict := func(d []byte) []byte {
		var opts []zstd.EncoderOption
		if d != nil {
			opts = append(opts, zstd.WithEncoderRawDict(d))
		}
		enc, err := zstd.NewEncoder(opts...)
		if err != nil {
			log.Fatal(err)
		}
		return enc.AppendCompress(nil, data)
	}

	without := compressWithDict(nil)
	with := compressWithDict(dict)

	dec, err := zstd.NewDecoder()
	if err != nil {
		log.Fatal(err)
	}

	got, err := dec.AppendDecompress(nil, without)
	if err != nil || string(got) != string(data) {
		log.Fatal("without dict mismatch")
	}
	fmt.Println("without dict: OK")

	dec, err = zstd.NewDecoder(zstd.WithDecoderRawDict(dict))
	if err != nil {
		log.Fatal(err)
	}
	got, err = dec.AppendDecompress(nil, with)
	if err != nil || string(got) != string(data) {
		log.Fatal("with dict mismatch")
	}
	fmt.Println("with dict: OK")
	fmt.Println("dict smaller:", len(with) < len(without))
	// Output:
	// without dict: OK
	// with dict: OK
	// dict smaller: true
}
