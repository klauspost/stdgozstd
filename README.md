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

Configuration is supplied through functional options that are shared by each
side's constructors: the same `EncoderOption` values configure `NewEncoder`
(one-shot) and `NewWriter` (streaming); likewise `DecoderOption` for
`NewDecoder` and `NewReader`. The one-shot `Encoder`/`Decoder` types are
immutable after construction; the streaming `Writer`/`Reader` can be
reconfigured on `Reset` (see below).

```go
type Encoder struct{ /* no exported fields */ }
type EncoderOption func(*Encoder) error
func NewEncoder(opts ...EncoderOption) (*Encoder, error)
func WithEncoderLevel(int) EncoderOption
func WithWindowSize(int) EncoderOption
func WithLowMemory(bool) EncoderOption
func WithEncoderCRC(bool) EncoderOption
func WithEncoderDict(*Dict) EncoderOption
func WithEncoderRawDict([]byte) EncoderOption
func WithContentSize(int64) EncoderOption       // streaming Writer only; declares total size
func (*Encoder) AppendCompress(dst, src []byte) []byte

type Decoder struct{ /* no exported fields */ }
type DecoderOption func(*Decoder) error
func NewDecoder(opts ...DecoderOption) (*Decoder, error)
func WithDecoderMaxSize(int64) DecoderOption    // total decompressed-output cap; 0 = unlimited
func WithDecoderMaxWindow(int) DecoderOption
func WithDecoderDict(*Dict) DecoderOption
func WithDecoderRawDict([]byte) DecoderOption
func (*Decoder) AppendDecompress(dst, src []byte) ([]byte, error)
```

The streaming `Writer`/`Reader` take the same options; with no options they use
default configuration:

```go
func NewWriter(w io.Writer, opts ...EncoderOption) (*Writer, error)
func NewReader(r io.Reader, opts ...DecoderOption) (*Reader, error)
func (*Writer) Reset(w io.Writer, opts ...EncoderOption) error
func (*Reader) Reset(r io.Reader, opts ...DecoderOption) error
```

Options are applied in order, so a later option overrides an earlier one (e.g.
`WithWindowSize` after `WithEncoderLevel`).

`Reset` reuses a `Writer`/`Reader` for a new stream. With no options it swaps the
destination/source and preserves the configuration; any options passed reconfigure
it for the new stream and persist until changed. Every option may be changed this
way. Reconfiguration only happens on `Reset`, never mid-stream.

`WithDecoderMaxSize(n)` caps the total number of decompressed bytes as a
decompression-bomb guard. The default, 0, disables the limit.

# Pre-PR

I have not added any significant test data yet.
I didn't want to bloat the standard library too much.
My plan was to add maybe around a MB of (compressed) fuzz base data.
This would then be the regression tests for the implementation.

`_testref` is sanify checks that crosschecks with [github.com/klauspost/compress/zstd](https://github.com/klauspost/compress/tree/master/zstd).
This will not be part of the code submitted to the Go repo.

I haven't done detailed benchmarking yet, outside that it looks reasonable compared to the upstream implementations.
