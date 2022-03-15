# Copyright Amazon.com Inc. or its affiliates. All Rights Reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License"). You may
# not use this file except in compliance with the License. A copy of the
# License is located at
#
#	 http://aws.amazon.com/apache2.0/
#
# or in the "license" file accompanying this file. This file is distributed
# on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
# express or implied. See the License for the specific language governing
# permissions and limitations under the License.

import dataclasses
import sys

import prettytable
import yaml

import controller
import maintenance_phases
import project_stages

FORMAT_TABLE = "table"
FORMAT_YAML = "yaml"
FORMATS = set([
    FORMAT_TABLE,
    FORMAT_YAML,
])


class Writer:
    def __init__(self, args):
        self._args = args

    def debug(self, *args, **kwargs):
        if self._args.debug:
            print(*args, **kwargs)

    def print_services(self, services, controllers):
        if self._args.output is None:
            self._outfile = sys.stdout
        else:
            self._outfile = open(self._args.output, 'w')
        if self._args.format == FORMAT_TABLE:
            self._print_table(services, controllers)
        else:
            self._print_yaml(services, controllers)

    def _print_table(self, services, controllers):
        num_services = len(services)
        num_preview = sum(
            [
                1 for c in controllers.values()
                if c.maintenance_phase == maintenance_phases.PREVIEW
            ]
        )
        num_ga = sum(
            [
                1 for c in controllers.values()
                if c.maintenance_phase == maintenance_phases.GENERAL_AVAILABILITY
            ]
        )
        num_controllers = num_preview + num_ga
        pct_coverage = round(((num_controllers / num_services) *100), 2)

        self._outfile.write(f"Number of AWS services with published APIs: {num_services}\n")
        self._outfile.write(f"Number of ACK controllers:                  {num_controllers}\n")
        self._outfile.write(f"Service coverage %:                         {pct_coverage}\n")

        self._outfile.write(f"Number controllers in PREVIEW:              {num_preview}\n")
        self._outfile.write(f"Number controllers in GENERAL_AVAILABILITY: {num_ga}\n")
        self._outfile.write("\n")

        t = prettytable.PrettyTable()
        t.field_names = [
            "Service",
            "Project Stage",
            "Maintenance Phase",
            "Latest version",
            "ACK runtime",
            "aws-sdk-go",
        ]
        t.align = "r"
        t.align["Service"] = "l"
        for c in controllers.values():
            service_name = c.service.package_name[0:30]
            proj_stage = c.project_stage
            maint_phase = c.maintenance_phase
            con_version = "n/a"
            if c.latest_release is not None:
                con_version = c.latest_release.controller_version or "n/a"
            runtime_version = "n/a"
            if c.latest_release is not None:
                runtime_version = c.latest_release.ack_runtime_version or "n/a"
            aws_sdk_version = "n/a"
            if c.latest_release is not None:
                aws_sdk_version = c.latest_release.aws_sdk_go_version or "n/a"

            t.add_row([
                service_name,
                proj_stage,
                maint_phase,
                con_version,
                runtime_version,
                aws_sdk_version,
            ])
        self._outfile.write(t.get_string())
        self._outfile.write("\n")

    def _print_yaml(self, services, controllers):
        obj = dict(
            controllers={
                cname: dataclasses.asdict(c)
                for cname, c in controllers.items()
            }
        )
        yaml.safe_dump(obj, self._outfile)
