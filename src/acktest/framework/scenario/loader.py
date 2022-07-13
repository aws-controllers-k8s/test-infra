import yaml
from pathlib import Path
from acktest.framework.scenario.model import ReplacementMap, ScenarioModel


def load_scenario(path: Path, replacements: ReplacementMap = {}) -> ScenarioModel:
    with open(path, 'r') as scenario_file:
        lines = scenario_file.read()

        for k, v in replacements.items():
            # Replace $<KEY> with <VALUE>
            lines = lines.replace(f'${k}', v)

        scen_dict = yaml.safe_load(lines)
    return ScenarioModel(**scen_dict)