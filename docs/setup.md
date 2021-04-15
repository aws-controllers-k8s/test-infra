# ACK `test-infra` dev env setup

This document will walk you through setting up your ACK test-infra development
environment.

## Pre-requisites

We will use `pipx` to install some Python3 CLI tools that make it easy for us
to do isolated development on our local machines without requiring root
privileges or installing software outside the local user context.

Install and configure `pipx` like so:

```bash
python3 -m pip install --user pipx
python3 -m pipx ensurepath
```

Install the `pipenv` tool for creating Python3 virtual environments:

```bash
pipx install pipenv
```

Install the `flake8`, `black` and `isort` code formatter and Python import
sorter utilities:

```bash
pipenv install black isort --pre
```

Install the module requirements:

```bash
pipenv install -r requirements.txt
```

## Installing locally for service controllers

Each of the service controllers' integration test suites references a version of
`test-infra` from the git repository, directly. It is possible to require that
the integration tests directly reference the local copy of `test-infra`.

From within the `SERVICE-controller/test/e2e/` directory (and within its
respective Python environment!), first uninstall the current version of the 
common test module:
```bash
pip uninstall acktest
```

Now install it, linking it as editable, from the source directory:
```bash
pip install -e $GOPATH/src/github.com/aws-controllers-k8s/test-infra
```

Verify it was linked correctly by checking the `pip list` listing:
```bash
pip list | grep aws-controllers-k8s
```
