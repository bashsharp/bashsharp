# The corpus shape: a numbered descriptor over a process substitution feeds a
# loop; the shell opens the FIFO, `read -u` consumes it.
while read -ru3 x
do
	echo -n :
done 3< <(echo x)
echo
echo done
