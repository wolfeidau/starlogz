resource "aws_acm_certificate" "starlogz" {
  domain_name       = local.service_hostname
  validation_method = "DNS"

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_route53_record" "cert_validation" {
  for_each = {
    for dvo in aws_acm_certificate.starlogz.domain_validation_options : dvo.domain_name => {
      name   = dvo.resource_record_name
      type   = dvo.resource_record_type
      record = dvo.resource_record_value
    }
  }

  zone_id = var.zone_id
  name    = each.value.name
  type    = each.value.type
  records = [each.value.record]
  ttl     = 60
}

resource "aws_acm_certificate_validation" "starlogz" {
  certificate_arn         = aws_acm_certificate.starlogz.arn
  validation_record_fqdns = [for r in aws_route53_record.cert_validation : r.fqdn]
}

resource "aws_route53_record" "starlogz" {
  zone_id = var.zone_id
  name    = local.service_hostname
  type    = "A"

  alias {
    name                   = aws_apigatewayv2_domain_name.starlogz.domain_name_configuration[0].target_domain_name
    zone_id                = aws_apigatewayv2_domain_name.starlogz.domain_name_configuration[0].hosted_zone_id
    evaluate_target_health = false
  }
}
