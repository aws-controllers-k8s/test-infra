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

Install the `pytest` tool:

```bash
pipenv install pytest --pre
```
