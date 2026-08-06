"""HermesRuntime SDK exceptions."""


class HermesRuntimeError(Exception):
    """Base class for hermes_runtime errors."""


class RuntimeBinaryNotFound(HermesRuntimeError):
    """The ``xhermes`` console script could not be located."""


class RuntimeStartError(HermesRuntimeError):
    """The headless backend failed to start or announce its port."""
