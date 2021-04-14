from setuptools import setup, find_packages
import pathlib

here = pathlib.Path(__file__).parent.resolve()

long_description = (here / 'README.md').read_text(encoding='utf-8')
install_requirements = (here / 'requirements.txt').read_text(encoding='utf-8').splitlines()

setup(
    name='acktest',
    version='0.0.1',
    description='Test framework for functional integration and soak testing for ACK service controllers',
    long_description=long_description,
    long_description_content_type='text/markdown',
    url='https://github.com/aws-controllers-k8s/test-infra', 
    classifiers=[
        'Programming Language :: Python :: 3',
        'Programming Language :: Python :: 3.8',
        'License :: OSI Approved :: Apache Software License',
        'Operating System :: OS Independent',
    ],
    package_dir={'':'src'},
    packages=find_packages('src'),
    python_requires='>=3.8, <4',
    project_urls={
        'Bug Reports': 'https://github.com/aws-controllers-k8s/community/issues',
    },
    install_requires=install_requirements
)
