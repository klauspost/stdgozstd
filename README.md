# Go zstd library

This is a proposal of implementation for `compress/zstd`.

See https://github.com/golang/go/issues/62513

It is based off [github.com/klauspost/compress/zstd](https://github.com/klauspost/compress/tree/master/zstd) 
but rewritten for the interface of outlined in the issue above.

Performance is somewhere in the area of 5-10% slower than the upstream Go implementations.
Mainly due to code simplification for easier maintainability.

## Notable differences to upstream:

* Fully single-threaded.
* All assembly removed.
* Simplified errors.
* Dictionary code simplified.
* All "unsafe" removed.
* Allows using the zero value Reader/Writer (not proposed explicitly, but seems reasonable).

## Additions to proposal

Levels 0 -> 9 are mapped to 4 internal levels:

| Level | Encoder           | Window |
|-------|-------------------|--------|
| 0     | raw blocks        | —      |
| 1–2   | fastEncoder       | 4/8MB  |
| 3–4   | doubleFastEncoder | 4/8MB  |
| 5–7   | betterFastEncoder | 8MB    |
| 8–9   | bestFastEncoder   | 8MB    |

Default is level 3 similar to zstandard. 

We *can* make changes to the individual levels, but that will require some amount of code duplication, 
or slower processing due to more branching.
Given that levels 1 + default + 9 by far are the most common, it is not a top priority.

We can also reduce the lower levels default window size to somewhere between 1-2MB, 
though it will only affect RAM usage, not speed in particular.

Supporting `io.WriterTo` and `io.ReaderFrom` on `Writer` and `Reader`:

* Added `(*Writer).ReadFrom(r io.Reader) (int64, error)`
* Added `(*Reader).WriteTo(w io.Writer) (int64, error)`

Bytes interface:

* Added `(*Writer) AppendTo(dst, src []byte) []byte`
* Added `(*Reader).DecodeBytes(dst, src []byte) ([]byte, error)`

# Pre-PR

I have not added any significant test data yet.
I didn't want to bloat the standard library too much.
My plan was to add maybe around a MB of (compressed) fuzz base data.
This would then be the regression tests for the implementation.

`_testref` is sanify checks that crosschecks with [github.com/klauspost/compress/zstd](https://github.com/klauspost/compress/tree/master/zstd).
This will not be part of the code submitted to the Go repo.

I haven't done detailed benchmarking yet, outside that it looks reasonable compared to the upstream implementations.