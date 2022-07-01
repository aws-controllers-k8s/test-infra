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

"""Utilities for working with tags"""

from typing import List, Union

ACK_SYSTEM_TAG_PREFIX = "services.k8s.aws/"
ACK_SYSTEM_CONTROLLER_VERSION_TAG_KEY = f'{ACK_SYSTEM_TAG_PREFIX}controller-version'
ACK_SYSTEM_NAMESPACE_TAG_KEY = f'{ACK_SYSTEM_TAG_PREFIX}namespace'


def assert_ack_system_tags(tags: Union[dict, list],
                           key_member_name: str = 'Key',
                           value_member_name: str = 'Value'
                           ):
    """
    assert_ack_system_tags verifies that ACK system tags are present inside
    'tags' parameter.
    If 'tags' is List of Tags, then 'key_member_name' and 'value_member_name'
    represent the field name for accessing TagKey and TagValue inside the Tag shape.
    Default for 'key_member_name' is 'Key' and default for 'value_member_name' is 'Value'
    """
    expected_tag_keys = [
        ACK_SYSTEM_CONTROLLER_VERSION_TAG_KEY,
        ACK_SYSTEM_NAMESPACE_TAG_KEY
    ]
    assert_keys_present(
        expected_tag_keys=expected_tag_keys,
        actual_tags=tags,
        key_member_name=key_member_name,
        value_member_name=value_member_name
    )


def assert_keys_present(expected_tag_keys: List[str],
                        actual_tags: Union[dict, list],
                        key_member_name: str = 'Key',
                        value_member_name: str = 'Value',
                        ):
    """
    assert_keys_present asserts that all the 'expected_tag_keys' are present
    inside 'actual_tags'.
    If 'actual_tags' is List of Tags, then 'key_member_name' and 'value_member_name'
    represent the field name for accessing TagKey and TagValue inside the Tag shape.
    Default for 'key_member_name' is 'Key' and default for 'value_member_name' is 'Value'
    """
    actual_tags_dict = to_dict(
        tags=actual_tags,
        key_member_name=key_member_name,
        value_member_name=value_member_name
    )
    for k in expected_tag_keys:
        assert k in actual_tags_dict,\
            f'Expected {k} to be present inside actual_tags'


def assert_present(expected: Union[dict, list],
                   actual: Union[dict, list],
                   key_member_name: str = 'Key',
                   value_member_name: str = 'Value',
                   ):
    """
    assert_present asserts that ALL the expected tags key-value pair are present
    inside actual tags.
    If 'expected' or 'actual' is List of Tags, then 'key_member_name' and 'value_member_name'
    represent the field name for accessing TagKey and TagValue inside the Tag shape.
    Default for 'key_member_name' is 'Key' and default for 'value_member_name' is 'Value'
    """
    actual_dict = to_dict(
        tags=actual,
        key_member_name=key_member_name,
        value_member_name=value_member_name
    )
    expected_dict = to_dict(
        tags=expected,
        key_member_name=key_member_name,
        value_member_name=value_member_name
    )
    for k in expected_dict.keys():
        assert k in actual_dict,\
            f'Expected {k} to be present in "actual" tags'
        assert (actual_dict[k] == expected_dict[k]),\
            f'For key: {k},values are different. expected: {expected_dict[k]}, actual: {actual_dict[k]}'


def assert_equal(expected: Union[dict, list],
                 actual: Union[dict, list],
                 key_member_name: str = 'Key',
                 value_member_name: str = 'Value',
                 ):
    """
    assert_equal asserts that ONLY the 'expected' tags key-value pair are present
    inside the 'actual' tags.
    If 'expected' or 'actual' is List of Tags, then 'key_member_name' and 'value_member_name'
    represent the field name for accessing TagKey and TagValue inside the Tag shape.
    Default for 'key_member_name' is 'Key' and default for 'value_member_name' is 'Value'
    """
    assert len(actual) == len(expected), \
        'length of "expected" and "actual" tags is not same'
    assert_present(
        expected=expected,
        actual=actual,
        key_member_name=key_member_name,
        value_member_name=value_member_name
    )


def assert_equal_without_ack_tags(expected: Union[dict, list],
                                  actual: Union[dict, list],
                                  key_member_name: str = 'Key',
                                  value_member_name: str = 'Value',
                                  ):
    """
    assert_equal_without_ack_tags asserts that ONLY the expected tags key-value pair are present
    inside actual tags, after ignoring the ACK system tags.
    If 'expected' or 'actual' is List of Tags, then 'key_member_name' and 'value_member_name'
    represent the field name for accessing TagKey and TagValue inside the Tag shape.
    Default for 'key_member_name' is 'Key' and default for 'value_member_name' is 'Value'

    """
    clean_expected = clean(tags=expected, key_member_name=key_member_name)
    clean_actual = clean(tags=actual, key_member_name=key_member_name)
    assert_equal(
        expected=clean_expected,
        actual=clean_actual,
        key_member_name=key_member_name,
        value_member_name=value_member_name
    )


def to_dict(tags: Union[dict, list],
            key_member_name: str = 'Key',
            value_member_name: str = 'Value',
            ) -> dict:
    """
    to_dict converts the tags into a dict representation.
    If 'tags' is List of Tags, then 'key_member_name' and 'value_member_name'
    represent the field name for accessing TagKey and TagValue inside the Tag shape.
    Default for 'key_member_name' is 'Key' and default for 'value_member_name' is 'Value'
    """
    if isinstance(tags, dict):
        return tags
    elif isinstance(tags, list):
        return {
            t[key_member_name]: t[value_member_name] for t in tags
        }
    else:
        raise RuntimeError('tags parameter can only be dict or list type')


def clean(tags: Union[dict, list],
          key_member_name: str = 'Key',
          ) -> Union[dict, list]:
    """
    Returns supplied tags collection, stripped of ACK system tags.
    If 'tags' is List of Tags, then 'key_member_name' represents the
    field name for accessing TagKey inside the Tag shape.
    Default for 'key_member_name' is 'Key'.
    """
    if isinstance(tags, dict):
        return {
            k: v for k, v in tags.items() if not k.startswith(ACK_SYSTEM_TAG_PREFIX)
        }
    elif isinstance(tags, list):
        return [
            t for t in tags if not t[key_member_name].startswith(ACK_SYSTEM_TAG_PREFIX)
        ]
    else:
        raise RuntimeError('tags parameter can only be dict or list type')
