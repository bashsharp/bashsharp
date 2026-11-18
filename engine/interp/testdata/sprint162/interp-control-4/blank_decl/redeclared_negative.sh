# Classic Bash++: a real name declared twice in one block is still refused —
# the diagnostic is reported and the second declaration does not take
# effect; only the blank identifier is exempt.
func main() {
 var a int = 1
 var a int = 2
 printf 'value %s\n' "$a"
}
main()
