# The shell itself opens a process-substitution FIFO for writing through a
# redirection; the substitution reads it and forwards to the original stdout.
echo out > >(cat)
wait
echo done
