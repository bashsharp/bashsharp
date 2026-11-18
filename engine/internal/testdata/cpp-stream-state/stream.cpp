#include <cstdio>
#include <cstdlib>
#include <fstream>
#include <iostream>
#include <sstream>
#include <stdexcept>
static std::ios::iostate& saved_state() { static auto value = std::ios::goodbit; return value; }
static std::ios::iostate& saved_exceptions() { static auto value = std::ios::goodbit; return value; }
static void done() {
    if (std::cout.rdstate() != saved_state() || std::cout.exceptions() != saved_exceptions()) {
        std::fputs("stream state changed\n", stderr);
        std::_Exit(91);
    }
    std::cout.exceptions(std::ios::goodbit);
    std::cout.clear();
    std::cout << "callback\n";
}
int example_main() {
    std::ios::sync_with_stdio(false);
    std::atexit(done);
    std::cout << "body\n";
    // STREAM_ACTION
    saved_state() = std::cout.rdstate();
    saved_exceptions() = std::cout.exceptions();
    // RETURN_ACTION
}
