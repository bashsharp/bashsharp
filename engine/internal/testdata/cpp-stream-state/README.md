# C++ stream-state regression fixtures

`L38.bsh` and `L144.bsh`, including their expected files, are byte-for-byte
copies from the Sprint 562 retained `cpp-by-example` corpus (tests baseline
`9f3a441920ab360e8a7625c3c48d1158d64db582`). `SHA256SUMS` matches the triage
packet's recorded source and golden hashes.

Original examples: MicrosoftDocs/cpp-docs,
`docs/standard-library/basic-ifstream-class.md`, lines 38 and 144, at
`281a0a0eae55fe97f5decd244db18ed5096d4687`. Code license: MIT.
The existing corpus wrapper is preserved, including its namespace and entry
function. No input text files are created; tests use empty working directories.

`stream.cpp` is a new synthetic regression template. Tests substitute its two
comment markers to exercise stream state, native failure and exception cases.
It is not an upstream example or a corpus adaptation.

`L49.bsh` and `L49.expected` retain the third same-root failure from
`docs/standard-library/fstream-typedefs.md#L49` at the same upstream pin.
Their hashes match the triage packet's `checkpoint-19.json`. The source tests
`!infile.bad()` before inserting a missing file's buffer into cout; no
`existingtextfile.txt` fixture is supplied or modified.
