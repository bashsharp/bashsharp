// Test-only exec wrapper: preserve argv and env while the child inherits an
// ignored TERM disposition. A shell wrapper may parse caller OPTIND itself.
#include <signal.h>
#include <stdio.h>
#include <unistd.h>

int main(int argc, char **argv) {
    if (argc < 2) {
        return 2;
    }
    if (signal(SIGTERM, SIG_IGN) == SIG_ERR) {
        perror("signal");
        return 1;
    }
    execv(argv[1], argv + 1);
    perror("execv");
    return 127;
}
