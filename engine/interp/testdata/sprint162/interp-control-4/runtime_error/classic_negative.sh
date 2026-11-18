# Classic Bash++ (not an original Go program): a division by zero inside a
# Go-form function stays the interpreter's own diagnostic — the update does
# not commit and the script continues — never a Go runtime panic.
func main() {
 var z int = 0
 var n int = 7
 n /= z
 printf 'unreachable %s\n' "$n"
}
main()
