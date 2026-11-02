"""Placeholder so pytest collection succeeds before W1."""


def test_version():
    from tierbound_worker import __version__

    assert __version__ == "0.1.0"
