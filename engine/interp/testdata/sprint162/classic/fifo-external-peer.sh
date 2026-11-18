# A named FIFO whose peer is an external process: the shell's write open
# must pair with cat's native read open, as in Bash.
mkfifo p
cat p &
echo x > p
wait
echo done
