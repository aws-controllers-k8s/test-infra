import re
import uuid
from typing import Any, List, Mapping, Union

from pydantic import BaseModel, Field, validator

ReplacementMap = Mapping[str, str]

def normalize_rfc_1123(val: str) -> str:
    if not re.search("^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$", val):
        raise ValueError("must match RFC 1123")
    return val

class ResourceModel(BaseModel):
    version: str
    group: str
    kind: str

class StepModel(BaseModel):
    pass

class NamedStepModel(StepModel):
    id: str = Field(default_factory=uuid.uuid4)
    description: str = ""

    _normalize_id = validator('id', allow_reuse=True)(normalize_rfc_1123)

class CreateStepProperties(BaseModel):
    spec: Any
    resource: ResourceModel = None

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

Step = Union[CreateStepModel, AssertStepModel, TemplateStepModel]

class ScenarioModel(BaseModel):
    id: str
    defaultResource: ResourceModel
    description: str = ""
    steps: List[Step] = []

    _normalize_id = validator('id', allow_reuse=True)(normalize_rfc_1123)