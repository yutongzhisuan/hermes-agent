"""Sub-agent integration code owned by XHermes.

Hosts the pieces of the sub-agent executor that depend on XHermes internals:
the in-process ACP backend, the ACP JSON-RPC sidecar, the stateless session
mode for untrusted remote tasks, and the structured output LLM helper. The
swarm-network worker talks to this side via the ``remote-acp`` backend and
the ``extend.sub_agent.acp_rpc_server`` sidecar.
"""
