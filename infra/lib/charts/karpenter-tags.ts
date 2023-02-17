import { Construct } from "constructs";
import { Chart } from "cdk8s";
import * as kplus from "cdk8s-plus-24";

export interface KarpenterTagsChartProps {
  readonly tagKey: string;
  readonly tagValue: string;
}

export class KarpenterTagsChart extends Chart {
  readonly configMap: kplus.ConfigMap;

  constructor(scope: Construct, id: string, props: KarpenterTagsChartProps) {
    super(scope, id, {
      namespace: "flux-system",
    });

    this.configMap = new kplus.ConfigMap(this, "TagsConfigMap", {
      metadata: {
        name: "karpenter-tags",
      },
      data: {
        tagKey: props.tagKey,
        tagValue: props.tagValue,
      },
    });
  }
}
