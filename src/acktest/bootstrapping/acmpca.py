import boto3

from dataclasses import dataclass, field

from . import Bootstrappable
from .. import resources

KEY_ALGORITHM = "RSA_2048"
SIGNING_ALGORITHM = "SHA256WITHRSA"
ROOT_CA_VALIDITY_DAYS = 3650


@dataclass
class PrivateCertificateAuthority(Bootstrappable):
    """An ACTIVE root private certificate authority.

    When allow_acm_renewal is set, the ACM service principal is granted the
    permissions it needs to renew certificates issued by the CA.
    """
    # Inputs
    name_prefix: str
    allow_acm_renewal: bool = False

    # Outputs
    common_name: str = field(init=False)
    arn: str = field(default="", init=False)

    def __post_init__(self):
        self.common_name = resources.random_suffix_name(self.name_prefix, 63)

    @property
    def acmpca_client(self):
        return boto3.client("acm-pca", region_name=self.region)

    def bootstrap(self):
        """Creates a root CA, installs its self-signed certificate and waits
        for it to become ACTIVE.
        """
        super().bootstrap()
        ca = self.acmpca_client.create_certificate_authority(
            CertificateAuthorityConfiguration={
                "KeyAlgorithm": KEY_ALGORITHM,
                "SigningAlgorithm": SIGNING_ALGORITHM,
                "Subject": {"CommonName": self.common_name},
            },
            RevocationConfiguration={
                "CrlConfiguration": {"Enabled": False},
                "OcspConfiguration": {"Enabled": False},
            },
            CertificateAuthorityType="ROOT",
        )
        self.arn = ca["CertificateAuthorityArn"]

        self.acmpca_client.get_waiter("certificate_authority_csr_created").wait(
            CertificateAuthorityArn=self.arn,
        )
        csr = self.acmpca_client.get_certificate_authority_csr(
            CertificateAuthorityArn=self.arn,
        )["Csr"]

        partition = self.arn.split(":")[1]
        issued = self.acmpca_client.issue_certificate(
            CertificateAuthorityArn=self.arn,
            Csr=csr,
            SigningAlgorithm=SIGNING_ALGORITHM,
            TemplateArn=f"arn:{partition}:acm-pca:::template/RootCACertificate/V1",
            Validity={"Value": ROOT_CA_VALIDITY_DAYS, "Type": "DAYS"},
        )
        self.acmpca_client.get_waiter("certificate_issued").wait(
            CertificateAuthorityArn=self.arn,
            CertificateArn=issued["CertificateArn"],
        )
        certificate = self.acmpca_client.get_certificate(
            CertificateAuthorityArn=self.arn,
            CertificateArn=issued["CertificateArn"],
        )["Certificate"]

        self.acmpca_client.import_certificate_authority_certificate(
            CertificateAuthorityArn=self.arn,
            Certificate=certificate,
        )

        if self.allow_acm_renewal:
            self.acmpca_client.create_permission(
                CertificateAuthorityArn=self.arn,
                Principal="acm.amazonaws.com",
                Actions=["IssueCertificate", "GetCertificate", "ListPermissions"],
            )

    def cleanup(self):
        """Disables the CA if it is ACTIVE and schedules it for deletion.

        Tolerant of partial state: bootstrap can fail before the CA is
        activated, and cleanup must not then raise and mask the original
        failure.
        """
        if self.arn:
            try:
                status = self.acmpca_client.describe_certificate_authority(
                    CertificateAuthorityArn=self.arn,
                )["CertificateAuthority"]["Status"]
                if status == "ACTIVE":
                    self.acmpca_client.update_certificate_authority(
                        CertificateAuthorityArn=self.arn,
                        Status="DISABLED",
                    )
                if status != "DELETED":
                    self.acmpca_client.delete_certificate_authority(
                        CertificateAuthorityArn=self.arn,
                        PermanentDeletionTimeInDays=7,
                    )
            except self.acmpca_client.exceptions.ResourceNotFoundException:
                pass
        super().cleanup()
