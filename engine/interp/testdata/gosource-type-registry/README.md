Byte-identical Go Tour originals from golang.org/x/website commit
c4a9d59f9775d994f1700d18fa37414c3c85fa7b, _content/tour.
BSD license retained in LICENSE. TestGoSourceOriginalTypeRegistryThreeModes
binds the original bytes with SHA-256 before executing each mode.

| file | tour path | bound by |
| --- | --- | --- |
| `index.go.txt` | `generics/index.go` | TestGoSourceOriginalTypeRegistryThreeModes |
| `list.go.txt` | `generics/list.go` | TestGoSourceOriginalTypeRegistryThreeModes |
| `slice-literals.go.txt` | `moretypes/slice-literals.go` | TestGoSourceOriginalTypeRegistryThreeModes |
| `exercise-web-crawler.go.txt` | `concurrency/exercise-web-crawler.go` | TestGoSourceOriginalForwardTypeThreeModes |
| `webcrawler.go.txt` | `solutions/webcrawler.go` | TestGoSourceOriginalForwardTypeRemainingGaps |
| `methods-errors.go.txt` | `methods/errors.go` | TestGoSourceOriginalForwardTypeRemainingGaps |

The last two still fail, for reasons outside declaration registration, and their
current diagnostics are pinned so the gaps stay visible:

- `webcrawler.go.txt` embeds `sync.Mutex` in an anonymous struct; the embedded
  imported method set is missing (`type struct has no method Lock`).
- `methods-errors.go.txt` gives `MyError` a `time.Time` field, which the
  dependency helper cannot materialise, so no local codec is emitted for it
  (`unregistered bridge type "*MyError"`).
