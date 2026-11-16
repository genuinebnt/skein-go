"""Pure domain logic for the agent: no I/O, no gRPC, no LangChain types.

Modules here are deterministic given their inputs (clocks and randomness are
passed in), so they test without fakes.
"""
