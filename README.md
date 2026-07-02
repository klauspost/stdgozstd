# Go zstd library

This is a proposal of implementation for `compress/zstd`.

See https://github.com/golang/go/issues/62513

It is based off [github.com/klauspost/compress/zstd](https://github.com/klauspost/compress/tree/master/zstd) 
but rewritten for the interface of outlined in the issue above.

Performance is somewhere in the area of 5-10% slower than the upstream Go implementations.
Mainly due to code simplification for easier maintainability.

Browse documentation: [![Go Reference](https://pkg.go.dev/badge/github.com/klauspost/stdgozstd.svg)](https://pkg.go.dev/github.com/klauspost/stdgozstd)

## Notable differences to upstream:

* Fully single-threaded.
* All assembly removed.
* Simplified errors.
* Dictionary code simplified.
* All "unsafe" removed.
* Allows using the zero value Encoder/Decoder/Reader/Writer (not proposed explicitly, but seems reasonable).

## Additions to proposal

Levels 0 -> 9 are mapped to 4 internal encoders with per-level window sizes:

| Level | Encoder     | Window |
|-------|-------------|--------|
| 0     | raw blocks  | —      |
| 1     | fast        | 2MB    |
| 2     | fast        | 3MB    |
| 3     | double-fast | 4MB    |
| 4     | double-fast | 4MB    |
| 5     | better      | 4MB    |
| 6     | better      | 5MB    |
| 7     | better      | 6MB    |
| 8     | best        | 7MB    |
| 9     | best        | 8MB    |

Default is level 3 similar to zStandard.
The window sizes roughly correspond to the defaults in zStandard.

Experience from deflate is that by far "fastest", "default" and "best" are used in practice.
So except for slight variations due to window sizes, levels 1, 3, 5 and 9 are the main ones.

More encoder levels are possible, but would come at a cost of either more code or slower processing. 

Supporting `io.WriterTo` and `io.ReaderFrom` on `Writer` and `Reader`:

* Added `(*Writer).ReadFrom(r io.Reader) (int64, error)`
* Added `(*Reader).WriteTo(w io.Writer) (int64, error)`

Bytes interface:

* Added `(*Encoder).AppendCompress(dst, src []byte) []byte`
* Added `(*Decoder).AppendDecompress(dst, src []byte) ([]byte, error)`

## Configuration split: Encoder / Decoder

Configuration and the one-shot byte operations live on two config types, so a
stream's settings can be prepared and reused independently of any single stream:

```go
type Encoder struct{ /* no exported fields */ }
func NewEncoder() *Encoder
func (*Encoder) SetLevel(int) error
func (*Encoder) SetWindowSize(int) error
func (*Encoder) SetLowMemory(bool)
func (*Encoder) SetCRC(bool)
func (*Encoder) AddDict(*Dict)
func (*Encoder) SetRawDict([]byte)
func (*Encoder) AppendCompress(dst, src []byte) []byte

type Decoder struct{ /* no exported fields */ }
func NewDecoder() *Decoder
func (*Decoder) SetMaxSize(int64) error       // total decompressed-output cap; 0 = unlimited
func (*Decoder) SetMaxWindowSize(int) error
func (*Decoder) AddDict(*Dict)
func (*Decoder) SetRawDict([]byte)
func (*Decoder) AppendDecompress(dst, src []byte) ([]byte, error)
```

The streaming `Writer`/`Reader` are bound to a config object; a nil argument
uses default configuration:

```go
func NewWriter(w io.Writer, e *Encoder) *Writer
func NewReader(r io.Reader, d *Decoder) *Reader
```

One `Encoder`/`Decoder` may configure many `Writer`/`Reader` values. It must not
be reconfigured while a bound `Writer`/`Reader` is mid-stream; reconfigure
between streams (before the first write, or after `Close`, then `Reset`).

`(*Decoder).SetMaxSize(n)` caps the total number of decompressed bytes as a
decompression-bomb guard. The default, 0, disables the limit.

# Pre-PR

I have not added any significant test data yet.
I didn't want to bloat the standard library too much.
My plan was to add maybe around a MB of (compressed) fuzz base data.
This would then be the regression tests for the implementation.

`_testref` is sanify checks that crosschecks with [github.com/klauspost/compress/zstd](https://github.com/klauspost/compress/tree/master/zstd).
This will not be part of the code submitted to the Go repo.

I haven't done detailed benchmarking yet, outside that it looks reasonable compared to the upstream implementations.
