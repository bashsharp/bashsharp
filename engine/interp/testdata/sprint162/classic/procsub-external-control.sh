# Positive control: the FIFO path is handed to an external command, which
# opens it natively. This already worked in both dialects.
cat <(echo viacat)
echo done
