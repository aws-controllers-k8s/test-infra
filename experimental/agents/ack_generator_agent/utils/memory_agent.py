from strands import Agent
from strands_tools import mem0_memory
from logging import getLogger
from typing import Optional
from ack_generator_agent.utils.constants import MEMORY_AGENT_SYSTEM_PROMPT

MEM0_USER_ID = "ack_codegen_agent_user"

class MemoryAgent:
    """Dedicated memory agent for ACK build error/solution management."""
    
    def __init__(self):
        self.logger = getLogger(__name__)
        self.user_id = MEM0_USER_ID
        self.agent = Agent(
            system_prompt=MEMORY_AGENT_SYSTEM_PROMPT,
            tools=[mem0_memory]
        )
    
    def store_error_solution(self, error_message: str, solution: str, metadata: dict = None) -> str:
        """Store an error and its solution in memory."""
        try:
            if metadata is None:
                metadata = {}
            metadata.update({
                "type": "error_solution",
                "error_message": error_message[:200],  # Truncate for metadata
            })
            
            # Format the content to include both error and solution
            content = f"ERROR: {error_message}\n\nSOLUTION: {solution}"
            
            # Use the agent to store the memory
            prompt = f"Store this error-solution pair in memory using action='store', user_id='{self.user_id}', content='{content}', metadata={metadata}"
            response = self.agent(prompt)
            
            self.logger.info(f"Stored error solution in memory: {error_message[:100]}...")
            return f"Successfully saved solution for error: {error_message[:100]}{'...' if len(error_message) > 100 else ''}"
        except Exception as e:
            self.logger.error(f"Failed to store error solution: {e}")
            return f"Error storing solution: {str(e)}"
    
    def lookup_error_solution(self, error_message: str) -> Optional[str]:
        """Look up a solution for an error message."""
        try:
            # Use the agent to search for memories
            prompt = f"Search for solutions to this error using action='retrieve', user_id='{self.user_id}', query='{error_message}', min_score=0.7, limit=3"
            response = self.agent(prompt)
            
            response_str = str(response)
            if "no memories" in response_str.lower() or "not found" in response_str.lower():
                self.logger.info(f"No solution found for error: {error_message[:100]}...")
                return None
            else:
                self.logger.info(f"Found solution for error: {error_message[:100]}...")
                return response_str
        except Exception as e:
            self.logger.error(f"Failed to lookup error solution: {e}")
            return f"Error looking up solution: {str(e)}"
    
    def search_memories(self, query: str, limit: int = 5) -> str:
        """Search through stored memories."""
        try:
            prompt = f"Search for memories related to '{query}' using action='retrieve', user_id='{self.user_id}', min_score=0.5, limit={limit}"
            response = self.agent(prompt)
            return str(response)
        except Exception as e:
            self.logger.error(f"Failed to search memories: {e}")
            return f"Error searching memories: {str(e)}"
    
    def add_knowledge(self, content: str, metadata: dict = None) -> str:
        """Add general knowledge to memory."""
        try:
            if metadata is None:
                metadata = {"type": "knowledge"}
            
            prompt = f"Store this knowledge in memory using action='store', user_id='{self.user_id}', content='{content}', metadata={metadata}"
            response = self.agent(prompt)
            
            return f"Successfully added knowledge: {content[:100]}{'...' if len(content) > 100 else ''}"
        except Exception as e:
            self.logger.error(f"Failed to add knowledge: {e}")
            return f"Error adding knowledge: {str(e)}"
    
    def list_all_memories(self) -> str:
        """List all stored memories."""
        try:
            prompt = f"List all memories using action='list', user_id='{self.user_id}'"
            response = self.agent(prompt)
            return str(response)
        except Exception as e:
            self.logger.error(f"Failed to list memories: {e}")
            return f"Error listing memories: {str(e)}"
