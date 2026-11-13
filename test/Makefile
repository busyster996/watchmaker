#!/usr/bin/env make -f

TOPDIR := $(realpath $(dir $(abspath $(lastword $(MAKEFILE_LIST)))))
SELF := $(abspath $(lastword $(MAKEFILE_LIST)))

TESTS_C = test_clock_gettime test_gettimeofday test_time
BIN_DIR := $(TOPDIR)/bin
TEST_BINARIES := $(addprefix $(BIN_DIR)/,$(TESTS_C))
FAKECLOCK_SOURCES = ../fakeclock/fake_clock_gettime.c ../fakeclock/fake_gettimeofday.c

build: $(TEST_BINARIES) $(BIN_DIR)/test_fakeclock

$(BIN_DIR):
	mkdir -p $@

$(TEST_BINARIES): $(BIN_DIR)/%: %.c | $(BIN_DIR)
	$(CC) -o $@ $<

$(TESTS_C): %: $(BIN_DIR)/%

test_fakeclock: $(BIN_DIR)/test_fakeclock

$(BIN_DIR)/test_fakeclock: test_fakeclock.c $(FAKECLOCK_SOURCES) | $(BIN_DIR)
	$(CC) -O2 -fno-builtin -Wall -Wextra -o $@ $^

run-test-fakeclock: $(BIN_DIR)/test_fakeclock
	timeout 5s $(BIN_DIR)/test_fakeclock

run-test-cpp:
	go test -count=1 -timeout 45s -run '^Test(SkewCPP|CPPThreadProgress|PtraceSignals)$$' ..

run-test-%: $(TEST_BINARIES)
	$(TOPDIR)/runtest.sh "$*" "$(TOPDIR)"

test: run-test-fakeclock run-test-cpp run-test-clock_gettime run-test-gettimeofday run-test-time

.PHONY: build test clean test_fakeclock $(TESTS_C) run-test-fakeclock run-test-cpp
clean:
	rm -rf $(BIN_DIR)
