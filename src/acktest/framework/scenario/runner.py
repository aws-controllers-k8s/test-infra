from typing import Any, Mapping
from uuid import uuid4
from acktest.framework.helper.register import HelperMap

from acktest.framework.scenario.model import CreateStepModel, NamedStepModel, ScenarioModel, TemplateStepModel
from acktest.k8s.resource import CustomResourceReference, create_resource

SCENARIO_LABEL_PREFIX = "test.ack.aws/"

SCENARIO_NAME_LABEL = SCENARIO_LABEL_PREFIX + "scenario"
SCENARIO_STEP_LABEL = SCENARIO_LABEL_PREFIX + "step"
SCENARIO_RUN_LABEL = SCENARIO_LABEL_PREFIX + "run-id"

class ScenarioRunner:
    """ Handles running through the steps of a scenario
    """
    _run_id: str
    _config: ScenarioModel
    _namespace: str
    _helpers: HelperMap

    _current_step_index: int = 0
    _step_resource_map: Mapping[str, CustomResourceReference] = {} 

    def __init__(self, config: ScenarioModel, helper_map: HelperMap, namespace: str = "default", run_id: str = ""):
        self._config = config
        self._helpers = helper_map
        self._namespace = namespace

        # Generate a weak UUID for the current scenario run
        self._run_id = run_id if run_id else str(uuid4())[:8]

        self.validate_helpers_exist()

    @property
    def id(self):
        return self._config.id

    @property
    def default_resource(self):
        return self._config.defaultResource

    def run_step(self):
        current_step = self._config.steps[self._current_step_index]

        if isinstance(current_step, TemplateStepModel):
            self._run_create_step(current_step)
        elif isinstance(current_step, CreateStepModel):
            self._run_create_step(current_step)
        else:
            raise NotImplementedError(f'step type {type(current_step).__name__} is not supported yet')

        self._current_step_index += 1

    def validate_helpers_exist(self):
        resources = [self.default_resource]

        # Build a list of all resources referenced within the scenario
        for step in self._config.steps:
            if isinstance(step, CreateStepModel) and step.create.resource:
                resources.append(step.create.resource)

        # Each of the resources exists in the helper map
        unimplemented = next(filter(lambda resource: not self._helpers.has(resource.kind, resource.version), resources), None)
        if unimplemented is not None:
            raise NotImplementedError(f'missing helper for {unimplemented}')

    def _get_step_resource_name(self, step: NamedStepModel):
        # Concatenate the scenario ID with the step ID
        return f"{self._run_id}-{self._config.id}-{step.id}"[-256:]

    def _get_step_labels(self, step: NamedStepModel):
        labels = {}
        labels[SCENARIO_NAME_LABEL] = self.id
        labels[SCENARIO_STEP_LABEL] = step.id
        labels[SCENARIO_RUN_LABEL] = self._run_id
        return labels

    def _run_create_step(self, step: CreateStepModel):
        spec = step.create.spec

        resource = step.create.resource if step.create.resource else self.default_resource
        resource_name = self._get_step_resource_name(step)

        # Lookup plural from resource helper
        helper = self._helpers.get(resource.kind, resource.version)

        ref = CustomResourceReference(
            name=resource_name,
            namespace=self._namespace,
            group=resource.group,
            version=resource.version,
            plural=helper.plural,
        )

        body = {
            "apiVersion": f"{ref.group}/{ref.version}",
            "kind": resource.kind,
            "metadata": {
                "name": resource_name,
                "namespace": self._namespace,
                "labels": self._get_step_labels(step)
            },
            "spec": spec
        }

        resp = create_resource(ref, body, 3, 5)

        if resp is not None:
            # Resource created successfully
            self._step_resource_map[step.id] = ref