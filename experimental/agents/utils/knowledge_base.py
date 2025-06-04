# Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License"). You may not use this file except in compliance
# with the License. A copy of the License is located at
#
#    http://www.apache.org/licenses/LICENSE-2.0
#
# or in the 'license' file accompanying this file. This file is distributed on an 'AS IS' BASIS, WITHOUT WARRANTIES
# OR CONDITIONS OF ANY KIND, express or implied. See the License for the specific language governing permissions
# and limitations under the License.
"""Knowledge base for ACK agents."""

import json
import os

import boto3


def retrieve_from_knowledge_base(
    text: str,
    numberOfResults: int = 10,
    score: float = 0.5,
    knowledgeBaseId: str = None,
    region: str = "us-west-2",
) -> str:
    """
    Retrieve information from a Bedrock knowledge base using RAG.

    This tool searches through a Bedrock knowledge base to find relevant information
    about code generation, ACK controllers, and related topics.

    Args:
        text: The search query text
        numberOfResults: Maximum number of results to return (default: 10)
        score: Minimum relevance score threshold (default: 0.5)
        knowledgeBaseId: The Bedrock knowledge base ID (uses environment variable if not provided)
        region: AWS region for the Bedrock service (default: us-east-1)

    Returns:
        str: Retrieved information from the knowledge base or error message
    """
    try:
        # Use provided knowledgeBaseId or fall back to environment variable
        kb_id = knowledgeBaseId or os.environ.get("BEDROCK_KNOWLEDGE_BASE_ID")

        if not kb_id:
            return "Error: No knowledge base ID provided. Set BEDROCK_KNOWLEDGE_BASE_ID environment variable or pass knowledgeBaseId parameter."

        bedrock_agent_runtime = boto3.client("bedrock-agent-runtime", region_name=region)

        response = bedrock_agent_runtime.retrieve(
            knowledgeBaseId=kb_id,
            retrievalQuery={"text": text},
            retrievalConfiguration={
                "vectorSearchConfiguration": {
                    "numberOfResults": numberOfResults,
                    "overrideSearchType": "HYBRID",  # Use both semantic and keyword search
                }
            },
        )

        # Process the results
        results = []
        for result in response.get("retrievalResults", []):
            relevance_score = result.get("score", 0.0)

            # Filter by minimum score
            if relevance_score >= score:
                content = result.get("content", {}).get("text", "")
                metadata = result.get("metadata", {})

                # Format the result
                result_text = f"Score: {relevance_score:.3f}\n"
                result_text += f"Content: {content}\n"

                # Add metadata if available
                if metadata:
                    result_text += f"Metadata: {json.dumps(metadata, indent=2)}\n"

                result_text += "---\n"
                results.append(result_text)

        if not results:
            return f"No results found for query '{text}' with minimum score {score}. Try lowering the score threshold or using different search terms."

        # Combine all results
        output = f"Found {len(results)} relevant results for '{text}':\n\n"
        output += "\n".join(results)

        return output

    except Exception as e:
        return f"Error retrieving from knowledge base: {str(e)}"
