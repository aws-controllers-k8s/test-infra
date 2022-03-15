# ack-discover utility CLI tool

The `ack-discover` tool collects information about AWS services and ACK
controllers, including the latest release of a controller, the verison of the
ACK runtime embedded in the controller, the version of aws-sdk-go used in the
controller, the project stage and the maintenance phase of the controller.

## Prerequisites

You will need a Github Personal Access Token (PAT) associated with a Github
username. I create a file called `~/.github/venv` that I use with this content:

```
export GITHUB_ACTOR=jaypipes
export GITHUB_TOKEN=ghp_*************************************
```

Create a virtualenv and install the required Python libraries:

```bash
python3 -m venv .venv
source .venv/bin/activate
pip install -r tools/ack-discover/requirements.txt
```

## Running `ack-discover`

Run `ack-discover` from your virtualenv after sourcing your Github Personal
Access Token:

```bash
source ~/.github/venv
source .venv/bin/activate
tools/ack-discover/ack-discover --debug
```
