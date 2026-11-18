The file reader.go.txt is the unchanged `_content/tour/methods/reader.go` from
the pinned Go Tour corpus used by Sprint 118. Its bytes, including the OMIT
build directive, are checked by TestGoSourceNativeReadSlicesThreeModes before
native Go, interpreter and compiled artifact execution. LICENSE is the upstream
Tour license. The `.txt` suffix keeps this original fixture out of package tests.
