# Negative-set control: the peer is a background subshell of this shell.
# Both ends are shell-owned; this already worked in both dialects.
mkfifo p
( echo bg-writer > p ) &
read -r line < p
echo "read: $line"
wait
echo done
