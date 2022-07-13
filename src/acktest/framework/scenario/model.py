from typing import Any, List, Mapping, Union

from pydantic import BaseModel

ReplacementMap = Mapping[str, str]

class StepModel(BaseModel):
    pass

class NamedStepModel(StepModel):
    id: str = ""
    description: str = ""

class CreateStepProperties(BaseModel):
    spec: Any

class CreateStepModel(NamedStepModel):
    create: CreateStepProperties

class AWSAssertion(BaseModel):
    success: str = ""

class AssertStepProperties(BaseModel):
    aws: AWSAssertion

class AssertStepModel(NamedStepModel):
    expect: AssertStepProperties

class TemplateStepModel(StepModel):
    template: str

class ResourceModel(BaseModel):
    apiVersion: str
    kind: str

Step = Union[CreateStepModel, AssertStepModel, TemplateStepModel]

class ScenarioModel(BaseModel):
    id: str
    defaultResource: ResourceModel
    description: str = ""
    steps: List[Step] = []