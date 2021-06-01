# Prow Images

This directory contains all of the images used for any of our Prow pre- or post-
submit jobs. 

## Building images

To build the images, run the following command:
```bash
./build-images.sh
```

This will tag each of the images with `prow/<type>` where `<type>` is the suffix
of the Dockerfile, for example `prow/test`.

## Releasing images

To push the images into an ECR public repository, first authenticate with the 
corresponding account that has access to the public repository.

To publish a version of the images to your own repository, run the following
command:

```bash
DOCKER_REPOSITORY=<my-repository-uri> ./push-image.sh <image type>
```
Replacing `<my-repository-uri>` with the URI of the repository and <image type>
with the type of Dockerfile you wish to push.

To publish a new version of the images to the **official ACK repository**, run
the following command:

```bash
VERSION=X.Y.Z ./push-image.sh <image type>
```
Replacing `X.Y.Z` with the SemVer version tag of the images.

*Note: Only ACK core contributors will have access to the official ACK
repository*