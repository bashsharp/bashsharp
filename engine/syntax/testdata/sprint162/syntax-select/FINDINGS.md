# Findings

| root | first cause | mechanism | status |
| --- | --- | --- | --- |
| testdir:chan/nonblock.go | select receive assignment rejected | `=` receive clause | fixed in pending commit |
| testdir:chan/select4.go | select receive assignment rejected | addressable selector receive target | fixed in pending commit |
| testdir:fixedbugs/bug225.go | select receive assignment rejected | blank receive target | fixed in pending commit |
| testdir:fixedbugs/bug312.go | select receive assignment rejected | interface receive target | fixed in pending commit |
| testdir:fixedbugs/issue43111.go | select receive assignment rejected | one- and two-value receive assignment | fixed in pending commit |
| testdir:fixedbugs/issue43292.go | select receive assignment rejected | interface receive assignment | fixed in pending commit |
| testdir:fixedbugs/issue8347.go | select receive assignment rejected | two-value receive assignment | fixed in pending commit |
| testdir:ken/chan.go | select receive assignment rejected | repeated receive assignment | fixed in pending commit |
| testdir:typeparam/mdempsky/18.go | select receive assignment rejected | generic interface receive assignment | fixed in pending commit |

## Requests to other seams

None.
