# The shell itself opens a process-substitution FIFO for reading through a
# redirection. Bash: the read pairs with the substitution's writer.
read -r x < <(echo hello)
echo "status=$?"
echo "x=$x"
echo done
